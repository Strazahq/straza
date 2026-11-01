package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/strazahq/straza/internal/store"
)

// connectCaller is who calls a connect route: a user on a session, or an
// admin API token, which is no user and so names the user it acts for on
// every request.
type connectCaller struct {
	userID string        // the session's user, empty for a token
	token  *apiTokenMeta // nil for a session
}

// id is the id a stored row and a record name as the setter: the session's
// user or the token.
func (c connectCaller) id() string {
	if c.token != nil {
		return c.token.ID
	}
	return c.userID
}

// stamp adds the caller to a connection record as setBy, with actorVia
// api-token when the caller is an admin API token, and answers data.
func (c connectCaller) stamp(data map[string]any) map[string]any {
	data["setBy"] = c.id()
	if c.token != nil {
		data["actorVia"] = "api-token"
	}
	return data
}

// requireConnect authenticates a connect route that an admin API token may
// call as well. A wat_ token under the Bearer scheme passes when its scope
// covers the apps area for the request's verb, as on the admin plane: a read
// needs apps:read, a write apps:write, and full grants both. A scope refusal
// answers 403 and writes no record, as the admin plane does. Every other
// credential takes requireUser.
func (a *App) requireConnect(next func(w http.ResponseWriter, r *http.Request, c connectCaller)) http.HandlerFunc {
	session := a.requireUser(func(w http.ResponseWriter, r *http.Request, userID string) {
		next(w, r, connectCaller{userID: userID})
	})
	return func(w http.ResponseWriter, r *http.Request) {
		raw, scheme := sessionCredential(r)
		if !strings.HasPrefix(raw, apiTokenPrefix) || scheme != schemeBearer {
			session(w, r)
			return
		}
		meta, scope, ok := a.verifyAPIToken(w, r, raw)
		if !ok {
			return
		}
		if allowed, needed := scope.AllowsArea("apps", r.Method); !allowed {
			what := "a change to a user's connection"
			if r.Method == http.MethodGet {
				what = "reading a user's connections"
			}
			apiError(w, http.StatusForbidden, fmt.Sprintf("this admin API token lacks the scope %s, which %s needs. "+
				"A person mints a token that holds it in the console under Settings, API tokens.", needed, what))
			return
		}
		next(w, r, connectCaller{token: &meta})
	}
}

// userByRef reads the user ref names, by username or else by id.
func (a *App) userByRef(ctx context.Context, ref string) (store.User, error) {
	u, err := a.store.Users().GetByUsername(ctx, ref)
	if err == nil {
		return u, nil
	}
	if u, err = a.store.Users().GetByID(ctx, ref); err != nil {
		return store.User{}, fmt.Errorf("unknown user %q", ref)
	}
	return u, nil
}

// connectTarget resolves the user a connect request acts for and answers the
// request itself when it cannot. A session acts for its own user unless ref
// names another, which actFor judges with 403 or 404. An admin API token is
// no user, so it must name one: 400 when ref is empty, 404 for an unknown
// user, and any other user passes, because the token's scope already
// admitted the request.
func (a *App) connectTarget(w http.ResponseWriter, r *http.Request, c connectCaller, ref string) (store.User, bool) {
	if c.token != nil && ref == "" {
		where := "the field user of the body"
		if r.Method == http.MethodGet || r.Method == http.MethodDelete {
			where = "the query parameter user"
		}
		apiError(w, http.StatusBadRequest, "an admin API token is not a user, so a request on it must name the user whose connection it reads or changes. "+
			"Name that user, by username or id, in "+where+".")
		return store.User{}, false
	}
	var target store.User
	var refusal string
	var err error
	if c.token != nil {
		target, err = a.userByRef(r.Context(), ref)
	} else {
		target, refusal, err = a.actFor(r.Context(), c.userID, ref)
	}
	switch {
	case err != nil:
		apiError(w, http.StatusNotFound, err.Error())
	case refusal != "":
		apiError(w, http.StatusForbidden, refusal)
	default:
		return target, true
	}
	return store.User{}, false
}

// tokenSignInRefusal is the sentence for an admin API token that starts a
// provider sign-in for target. A sign-in is stored only for the person who
// finishes it in a browser, so a person signs in on the Credentials tab, and
// an agent target gets agentSignInRefusal.
func (a *App) tokenSignInRefusal(target store.User, server, provider string) string {
	if !personUser(target) {
		return agentSignInRefusal(target, server, provider)
	}
	page := strings.TrimRight(a.cfg.Server.PublicURL, "/") + connectPagePath
	return fmt.Sprintf("a sign-in at %s is stored only for the person who finishes it in a browser, so an admin API token cannot start one for %s. "+
		"%s signs in on the Credentials tab of the self-service page: %s", provider, target.Username, target.Username, page)
}
