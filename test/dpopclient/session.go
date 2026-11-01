package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// harnessInfo is the harness a check-in names.
type harnessInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// attestation is an unmanaged check-in with no measurements.
type attestation struct {
	Managed bool              `json:"managed"`
	Hashes  map[string]string `json:"hashes"`
}

// clientStamp names this client on the session.
type clientStamp struct {
	Version string `json:"version"`
}

// checkinBody is the key-lane check-in of /v1/checkin.
type checkinBody struct {
	DeviceID    string      `json:"device_id"`
	Counter     int64       `json:"counter"`
	Harness     harnessInfo `json:"harness"`
	Attestation attestation `json:"attestation"`
	Client      clientStamp `json:"client"`
}

// refreshBody is the refresh of a key-bound session on /v1/checkin.
type refreshBody struct {
	SessionToken string      `json:"session_token"`
	Harness      harnessInfo `json:"harness"`
	Attestation  attestation `json:"attestation"`
	Client       clientStamp `json:"client"`
}

// sessionAnswer is the part of a check-in answer the client keeps.
type sessionAnswer struct {
	SessionID    string `json:"session_id"`
	SessionToken string `json:"session_token"`
}

// sessionFlags are the flags every command after enroll takes.
type sessionFlags struct {
	fs        *flag.FlagSet
	keyPath   *string
	statePath *string
}

// newSessionFlags returns the flag set of one command with -key and -state.
func newSessionFlags(name string) sessionFlags {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	return sessionFlags{fs: fs,
		keyPath:   fs.String("key", "", "the Ed25519 key file enroll created"),
		statePath: fs.String("state", "", "the state file enroll wrote")}
}

// open parses args and loads the key and the state.
func (f sessionFlags) open(args []string, out io.Writer) (*client, error) {
	if err := f.fs.Parse(args); err != nil {
		return nil, err
	}
	if *f.keyPath == "" || *f.statePath == "" {
		return nil, fmt.Errorf("%s needs -key and -state", f.fs.Name())
	}
	key, err := loadKey(*f.keyPath)
	if err != nil {
		return nil, err
	}
	st, err := loadState(*f.statePath, false)
	if err != nil {
		return nil, err
	}
	if st.DeviceID == "" {
		return nil, fmt.Errorf("the state file %s names no enrolled device. Run dpopclient enroll first", *f.statePath)
	}
	return newClient(&st, key, out), nil
}

// runCheckin checks in on the key lane with the next counter, or with the
// counter -counter names, and keeps the session.
func runCheckin(args []string, out io.Writer) error {
	f := newSessionFlags("checkin")
	harness := f.fs.String("harness", "", "the harness as name/version, for example claude-code/2.1.0")
	counter := f.fs.Int64("counter", 0, "the counter to send in place of the next one, for the key reuse test")
	c, err := f.open(args, out)
	if err != nil {
		return err
	}
	h, err := parseHarness(*harness)
	if err != nil {
		return err
	}
	next := c.st.Counter + 1
	if *counter > 0 {
		next = *counter
	}
	// A key holder never sends a counter twice, so the highest one sent is
	// kept before the request leaves.
	c.st.Counter = max(c.st.Counter, next)
	if err := saveState(*f.statePath, *c.st); err != nil {
		return err
	}
	body, err := json.Marshal(checkinBody{DeviceID: c.st.DeviceID, Counter: next, Harness: h,
		Attestation: attestation{Hashes: map[string]string{}}, Client: clientStamp{Version: "dpopclient"}})
	if err != nil {
		return fmt.Errorf("encode the check-in: %w", err)
	}
	a, _, err := c.do(request{method: http.MethodPost, path: "/v1/checkin", body: body, counter: next})
	if err == nil {
		err = keepSession(c.st, a)
	}
	if err == nil {
		c.st.Harness = *harness
	}
	return save(*f.statePath, c.st, err)
}

// runCall sends one request with the session token, under DPoP with a proof
// that hashes the token, or under Bearer with no proof.
func runCall(args []string, out io.Writer) error {
	f := newSessionFlags("call")
	method := f.fs.String("method", http.MethodGet, "the request method")
	path := f.fs.String("path", "", "the request path, for example /v1/decide")
	body := f.fs.String("body", "", "the JSON request body")
	bearer := f.fs.Bool("bearer", false, "send the token under Bearer with no proof")
	replay := f.fs.Bool("replay", false, "send the same proof a second time")
	c, err := f.open(args, out)
	if err != nil {
		return err
	}
	switch {
	case *path == "" || !strings.HasPrefix(*path, "/"):
		return errors.New("call needs -path, starting with /")
	case c.st.SessionToken == "":
		return errors.New("the state holds no session. Run dpopclient checkin first")
	case *bearer && *replay:
		return errors.New("-replay resends a proof, and -bearer sends none, so use one of them")
	}
	r := request{method: strings.ToUpper(*method), path: *path, authToken: c.st.SessionToken, bearer: *bearer}
	if *body != "" {
		r.body = []byte(*body)
	}
	if !*bearer {
		r.proofATH = c.st.SessionToken
	}
	_, proof, err := c.do(r)
	if err == nil && *replay {
		fmt.Fprintln(out, "-- sending the same proof a second time")
		err = c.resend(r, proof)
	}
	return save(*f.statePath, c.st, err)
}

// runRefresh refreshes the key-bound session with a proof whose ath hashes
// the session token in the body, and keeps the new token.
func runRefresh(args []string, out io.Writer) error {
	f := newSessionFlags("refresh")
	c, err := f.open(args, out)
	if err != nil {
		return err
	}
	if c.st.SessionToken == "" {
		return errors.New("the state holds no session. Run dpopclient checkin first")
	}
	h, err := parseHarness(c.st.Harness)
	if err != nil {
		return err
	}
	body, err := json.Marshal(refreshBody{SessionToken: c.st.SessionToken, Harness: h, // #nosec G117 -- the refresh body carries the session token to the server by design
		Attestation: attestation{Hashes: map[string]string{}}, Client: clientStamp{Version: "dpopclient"}})
	if err != nil {
		return fmt.Errorf("encode the refresh: %w", err)
	}
	a, _, err := c.do(request{method: http.MethodPost, path: "/v1/checkin", body: body, proofATH: c.st.SessionToken})
	if err == nil {
		err = keepSession(c.st, a)
	}
	return save(*f.statePath, c.st, err)
}

// resend sends r again with the proof an earlier request carried.
func (c *client) resend(r request, proof string) error {
	req, _, err := c.build(r, c.st.Nonce, proof)
	if err != nil {
		return err
	}
	_, err = c.send(req)
	return err
}

// keepSession records the session of an accepted check-in in st.
func keepSession(st *state, a answer) error {
	if a.status != http.StatusOK {
		return fmt.Errorf("the check-in was refused with %d, and the answer above says why", a.status)
	}
	var got sessionAnswer
	if err := json.Unmarshal(a.body, &got); err != nil || got.SessionToken == "" {
		return errors.New("the check-in answer carries no session token")
	}
	st.SessionID, st.SessionToken = got.SessionID, got.SessionToken
	return nil
}

// parseHarness splits name/version.
func parseHarness(s string) (harnessInfo, error) {
	name, version, _ := strings.Cut(s, "/")
	if name == "" {
		return harnessInfo{}, errors.New("name the harness as name/version, for example -harness claude-code/2.1.0")
	}
	return harnessInfo{Name: name, Version: version}, nil
}

// save writes st and returns the command's error, or the write's error when
// the command succeeded.
func save(path string, st *state, err error) error {
	if saveErr := saveState(path, *st); saveErr != nil && err == nil {
		return saveErr
	}
	return err
}
