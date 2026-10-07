package ctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"slices"
)

// ErrNotLoggedIn is what Logout reports when this machine holds no credentials
// file at all: there is no session to revoke and nothing to delete, so the
// caller can say "nothing to log out of" instead of a file-not-found.
var ErrNotLoggedIn = errors.New("not logged in")

// LogoutResult reports what each half of a logout did. The server half is best
// effort and its failure is data, not an error; see Logout.
//
// It is a LIST of sessions on purpose: refreshing a dead session token
// re-establishes rather than refreshes, which mints a second session in the
// middle of the logout, so one logout can have to revoke two ids and can
// succeed for one and fail for the other.
type LogoutResult struct {
	// Path is the credentials file logout acted on.
	Path string
	// Revoked holds the sessions the server confirmed revoked, in the order
	// they were asked about.
	Revoked []string
	// Unrevoked holds the sessions that may still be live: the server refused,
	// was unreachable, or answered an error. Empty when nothing fell short.
	Unrevoked []string
	// RevokeErr is the first reason the server half fell short (the cause to
	// print beside Unrevoked). Nil when every session asked about was revoked.
	RevokeErr error
}

// Logout ends this machine's strazactl session: it asks strazad to revoke the
// session recorded in the credentials file (and any session the attempt itself
// minted; see revokeOwnSessions), then deletes the file.
//
// The two halves are deliberately unequal. Revocation is best effort: an
// unreachable deployment or an expired login (or, against a pre-0.41.0 server
// whose only revoke route is admin-gated, a caller without straza-admin) must
// never leave a live credential sitting on disk, so the shortfall comes
// back in LogoutResult.Unrevoked + RevokeErr for the caller to print, naming
// the sessions that may still be live. Deleting the credentials is the
// promise: only that half failing returns an error, and then the file is still
// there. A missing file returns ErrNotLoggedIn. Logout acts on the stored login
// alone: it never sends APIToken, and AgentMarker does not hold it back.
func (c *Client) Logout(ctx context.Context) (LogoutResult, error) {
	// Ending a session takes access away and changes nothing else, so the
	// guard has nothing to stop, and the token has no session to end.
	login := *c
	login.APIToken, login.AgentMarker = "", ""
	c = &login
	res := LogoutResult{Path: c.CredsPath}
	if _, err := os.Stat(c.CredsPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return res, ErrNotLoggedIn
		}
		return res, err
	}

	creds, err := c.loadCreds()
	switch {
	case err != nil:
		// A credentials file nobody can parse is precisely the state logout has
		// to be able to clear, so this is a reported half, not a failure.
		res.RevokeErr = err
	case creds.SessionID == "":
		res.RevokeErr = errors.New("credentials record no session id")
	case c.Base == "":
		res.RevokeErr = errors.New("credentials name no server")
	default:
		res.Revoked, res.Unrevoked, res.RevokeErr = c.revokeOwnSessions(ctx, creds.SessionID)
	}

	if err := os.Remove(c.CredsPath); err != nil {
		return res, fmt.Errorf("delete %s: %w", c.CredsPath, err)
	}
	return res, nil
}

// revokeOwnSessions revokes the stored session using the stored credentials,
// and then whatever session the credentials name AFTERWARDS. It returns the
// ids confirmed revoked, the ids that may still be live, and the first failure.
//
// Route preference: the SELF-SCOPED revoke first (POST /v1/session/revoke ends
// the session the presented token itself names, for ANY authenticated caller),
// falling back to the admin route only on a 404, which means a server too old
// to carry the route and nothing else. The admin route still serves the one id
// the self route structurally cannot name: the STORED one, after a mid-logout
// re-establish swapped the token.
//
// The re-read runs whether the first revoke succeeded or FAILED: a session
// token dies after 300 s, so the refresh usually falls back to the device
// credential, which does not refresh but MINTS a session and writes it to the
// credentials file. Returning early would delete the file with that one live.
func (c *Client) revokeOwnSessions(ctx context.Context, id string) (revoked, unrevoked []string, first error) {
	adminOnly := false // latched by the first 404: this server predates the self route
	attempt := func(id string) {
		if slices.Contains(revoked, id) {
			return // a self revoke already ended it under its own name
		}
		var err error
		if !adminOnly {
			actual, selfErr := c.selfRevoke(ctx)
			switch {
			case errors.Is(selfErr, errSelfRevokeUnsupported):
				adminOnly = true
			case selfErr != nil:
				// Any other self-route failure is THE outcome for this id;
				// rerouting it would turn every server error into a second,
				// usually-403 admin call and misname the cause.
				err = selfErr
			case actual == id:
				revoked = append(revoked, id)
				return
			default:
				// The reauth inside selfRevoke re-established, so the self
				// route ended the MINTED session, not the one asked about.
				// Record that kill, then let the admin route try the
				// asked-about id, the only route that can still name it.
				revoked = append(revoked, actual)
			}
		}
		if err == nil {
			if err = c.RevokeSession(ctx, id); err == nil {
				revoked = append(revoked, id)
				return
			}
		}
		unrevoked = append(unrevoked, id)
		if first == nil {
			first = err
		}
	}
	attempt(id)
	if after, err := c.loadCreds(); err == nil && after.SessionID != "" && after.SessionID != id {
		attempt(after.SessionID)
	}
	return revoked, unrevoked, first
}

// errSelfRevokeUnsupported marks a server without POST /v1/session/revoke
// (pre-0.41.0), the ONLY condition that sends logout to the admin route.
var errSelfRevokeUnsupported = errors.New("server predates the self-scoped session revoke")

// selfRevoke asks the server to end the session the currently stored token
// names (POST /v1/session/revoke), reauthing once when the token is refused
// and retrying with whatever the reauth stored. Returns the id the server
// confirmed revoked, which is not always the id the caller had in mind: a
// reauth that had to re-establish minted a fresh session, and the self route
// then ends THAT one (the response echoes which).
//
// The token is sent as-is first, with no expiring-soon pre-refresh: a token
// within its TTL ends its own session without minting anything, which is the
// whole economy of the route. A pre-refresh would create a new session that
// logout then has to chase.
func (c *Client) selfRevoke(ctx context.Context) (string, error) {
	creds, err := c.loadCreds()
	if err != nil {
		return "", err
	}
	buf, code, err := c.doRaw(ctx, http.MethodPost, "/v1/session/revoke", creds.SessionToken, nil)
	if err != nil {
		return "", err
	}
	if code == http.StatusUnauthorized {
		if err := c.reauth(ctx, creds); err != nil {
			return "", err
		}
		if creds, err = c.loadCreds(); err != nil {
			return "", err
		}
		if buf, code, err = c.doRaw(ctx, http.MethodPost, "/v1/session/revoke", creds.SessionToken, nil); err != nil {
			return "", err
		}
	}
	if code == http.StatusNotFound {
		// The route itself is missing. The server-side contract keeps this
		// unambiguous: a 0.41.0+ strazad never 404s a self revoke (a verified
		// token whose session is already gone still answers 200).
		return "", errSelfRevokeUnsupported
	}
	var out struct {
		Session string `json:"session"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(buf, &out)
	if code != http.StatusOK {
		if out.Error == "" {
			out.Error = fmt.Sprintf("HTTP %d", code)
		}
		return "", errors.New(out.Error)
	}
	if out.Session == "" {
		return "", fmt.Errorf("self revoke answered 200 without naming the session")
	}
	return out.Session, nil
}
