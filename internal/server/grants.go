package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/redact"
	"github.com/strazahq/straza/internal/store"
)

// The git grant routes' operator sentences. straza service relays each one
// to the person who ran git, unchanged.
const (
	grantUnboundMsg = "Straza gives git grants only to a session bound to this machine's key, and this session has none. " +
		"Run git on a machine where `straza service` runs."
	grantBadRequestMsg = "Straza could not read this git grant request, because %s. " +
		"Update straza on this machine to the version of the Straza server, then run git again."
	grantURLQueryMsg = "Straza: the git URL carries a query or a fragment, so Straza cannot name a repo in a git grant for it. " +
		"Set the git remote to the repo's plain URL, such as https://github.com/<owner>/<name>.git, then run git again."
	grantNoServerMsg = "Straza: no MCP server that you can use serves git for %s, so Straza gives no git grant for it. " +
		"Ask an administrator which MCP server serves git for this repo, and for a role on it."
	grantNotListedMsg = "Straza: %[1]s is not in Straza's list of %[2]s's repos yet, so Straza cannot name it in a git grant. " +
		"Straza asked %[2]s for its list again. Run git again in a minute, and if it keeps failing, ask an administrator to check the server's Repos tab."
	grantAmbiguousMsg = "Straza: two MCP servers, %s and %s, list %s for git, so Straza cannot pick one. " +
		"Ask an administrator to keep the repo on one server."
	grantOpUnmappedMsg = "Straza: the MCP server %[1]s serves no git %[2]s, because its git block maps no tool to %[2]s. " +
		"Ask an administrator to map %[2]s to a tool in the server's git block, then run git again."
	grantInvisibleMsg = "Straza refused this git %[1]s: none of your roles allows %[2]s on the MCP server %[3]s. " +
		"Ask an administrator for a role that allows %[2]s on %[3]s, then run git again."
	grantTooLargeMsg = "Straza: this push names %d ref updates, which is more than one git grant can carry, so it did not run. " +
		"Push fewer branches at a time, for example one git push per branch."
	grantMintFailedMsg = "Straza could not sign the git grant, so git did not run. " +
		"Run git again, and if it keeps failing, ask an administrator to look up this answer's correlation id in the strazad log."
)

// gitRepo is the repo a git URL names on the one server that serves it:
// the server's name, its git block, the repo's canonical owner/name, and
// its object type and id in the server's list.
type gitRepo struct {
	app     string
	git     manager.GitSpec
	repo    string
	typ, id string
}

// grantRequest is the body of POST /v1/grants. Only Upstream is read on
// POST /v1/grants/resolve.
type grantRequest struct {
	Upstream    string           `json:"upstream"`
	Op          string           `json:"op"`
	Refs        []authn.GrantRef `json:"refs"`
	WaitSeconds *int             `json:"waitSeconds"`
}

// handleGrantResolve answers POST /v1/grants/resolve: the one server that
// serves git for the URL, with the repo's canonical name and the grants'
// audience. It decides nothing and writes no audit record.
func (a *App) handleGrantResolve(w http.ResponseWriter, r *http.Request) {
	_, sub, _, ok := a.grantSession(w, r)
	if !ok {
		return
	}
	var body grantRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Upstream == "" {
		apiError(w, http.StatusBadRequest, fmt.Sprintf(grantBadRequestMsg, "its body is not a JSON object with the upstream URL"))
		return
	}
	if queried(body.Upstream) {
		apiError(w, http.StatusBadRequest, grantURLQueryMsg)
		return
	}
	repo, status, msg := a.resolveGit(sub, body.Upstream)
	if status != http.StatusOK {
		writeGitRefusal(w, status, msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"srv": repo.app, "repo": repo.repo, "aud": repo.git.URL})
}

// handleGrant answers POST /v1/grants: it resolves the repo as the resolve
// route does, decides the call of the tool the op maps to with the shared
// gateway decision, and answers a signed git grant whose audit record
// entered the queue first, the approval's pending ref, or the refusal.
func (a *App) handleGrant(w http.ResponseWriter, r *http.Request) {
	claims, sub, person, ok := a.grantSession(w, r)
	if !ok {
		return
	}
	var body grantRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apiError(w, http.StatusBadRequest, fmt.Sprintf(grantBadRequestMsg, "its body is not a JSON object"))
		return
	}
	if queried(body.Upstream) {
		apiError(w, http.StatusBadRequest, grantURLQueryMsg)
		return
	}
	if why := body.fault(); why != "" {
		apiError(w, http.StatusBadRequest, fmt.Sprintf(grantBadRequestMsg, why))
		return
	}
	repo, status, msg := a.resolveGit(sub, body.Upstream)
	if status != http.StatusOK {
		writeGitRefusal(w, status, msg)
		return
	}
	tool := repo.git.Tool(body.Op)
	if tool == "" {
		apiError(w, http.StatusForbidden, fmt.Sprintf(grantOpUnmappedMsg, repo.app, body.Op))
		return
	}
	// A grant the mint would refuse must not spend an approval, so the
	// grant is signed once with stand-in ids before the decision runs.
	g := authn.Grant{Audience: repo.git.URL, Subject: claims.Subject, Session: claims.Session, Harness: claims.Harness,
		Kind: grantKind(sub.UserType, person), JKT: claims.CNF, Server: repo.app, ObjType: repo.typ, ObjValue: repo.repo,
		ObjID: repo.id, Op: body.Op, Refs: body.Refs, Decision: uuid.NewString(), Approval: uuid.NewString()}
	if _, _, err := a.tokens.MintGrant(g); err != nil {
		a.refuseMint(w, r, err, len(body.Refs))
		return
	}
	oc, _ := a.manager.ObjectsFor(repo.app, tool)
	wait := a.gatewayHoldMax()
	if body.WaitSeconds != nil {
		wait = min(wait, time.Duration(*body.WaitSeconds)*time.Second)
	}
	v := a.decideCall(r.Context(), gatewayCall{claims: claims, sub: sub, name: tool,
		args: grantArgs(oc.Reach.Argument, repo.repo, body.Refs),
		grant: &grantMode{app: repo.app, tool: tool, op: body.Op, wait: wait,
			advertise: body.Op == manager.GitOpPush && len(body.Refs) == 0}})
	switch {
	case v.pending:
		writeJSON(w, http.StatusAccepted, map[string]any{"pending": map[string]string{
			"ref": v.approval.ref, "expiresAt": v.approval.expiresAt.UTC().Format(time.RFC3339)}})
	case v.refusal != "":
		apiError(w, http.StatusForbidden, v.refusal)
	default:
		a.issueGrant(w, r, g, repo, v)
	}
}

// grantSession authenticates a git grant route as every gateway request is
// authenticated and then requires a key-bound session, because a grant
// names the session's key. It also answers whether check-in found the user
// to be a person. It answers the refusal itself.
func (a *App) grantSession(w http.ResponseWriter, r *http.Request) (authn.Claims, policy.Subject, bool, bool) {
	claims, sub, ok := a.gatewayAuth(w, r)
	if !ok {
		return authn.Claims{}, policy.Subject{}, false, false
	}
	if claims.CNF == "" {
		apiError(w, http.StatusForbidden, grantUnboundMsg)
		return authn.Claims{}, policy.Subject{}, false, false
	}
	e, ok := a.subjects.forToken(claims)
	if !ok {
		apiError(w, http.StatusUnauthorized, sessionStateExpiredMsg)
		return authn.Claims{}, policy.Subject{}, false, false
	}
	return claims, sub, e.facts.person, true
}

// issueGrant signs g for an allowed verdict, with the object the gate
// decided on, queues its record and answers it. The record's CloudEvents
// id is the grant's dec claim, so the id comes first. A grant whose record
// the queue refuses never leaves the server.
func (a *App) issueGrant(w http.ResponseWriter, r *http.Request, g authn.Grant, repo gitRepo, v gatewayVerdict) {
	id := uuid.NewString()
	g.Decision, g.Approval = id, v.approval.ref
	g.ObjType, g.ObjValue, g.ObjID = "", "", ""
	if o := v.gate.Object; o != nil {
		g.ObjType, g.ObjValue, g.ObjID = o.Type, o.Value, o.ID
	}
	signed, minted, err := a.tokens.MintGrant(g)
	if err != nil {
		a.refuseMint(w, r, err, len(g.Refs))
		return
	}
	rec := v.rec
	rec.id = id
	rec.grant = &grantMember{jti: minted.JTI, op: g.Op, aud: repo.git.URL, exp: minted.Expiry, approval: v.approval.ref}
	if err := a.spoolMCP(r.Context(), rec); err != nil {
		a.fail(w, r, http.StatusServiceUnavailable, auditQueueFullMsg, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"grant": signed, "aud": repo.git.URL, "repo": repo.repo,
		"srv": repo.app, "exp": minted.Expiry.UTC().Format(time.RFC3339)})
}

// refuseMint answers a MintGrant error. A grant too long for one push is
// the caller's to split, so it answers 400 with the sentence that says so.
// Any other error means the route built the grant wrong, so its text goes
// to the log and never to the caller.
func (a *App) refuseMint(w http.ResponseWriter, r *http.Request, err error, refs int) {
	if errors.Is(err, authn.ErrGrantTooLarge) {
		apiError(w, http.StatusBadRequest, fmt.Sprintf(grantTooLargeMsg, refs))
		return
	}
	a.fail(w, r, http.StatusInternalServerError, grantMintFailedMsg, err)
}

// fault answers why b is not a grant request Straza can decide, or "" when
// it is one: an op of fetch or push, refs on a push only, at most 1000 of
// them, each a full ref name with ids of one hash length, and a wait that
// is not negative. The checks come before the decision, so a request the
// mint would refuse never spends an approval.
func (b grantRequest) fault() string {
	switch {
	case b.Upstream == "":
		return "it names no upstream URL"
	case b.Op != manager.GitOpFetch && b.Op != manager.GitOpPush:
		return fmt.Sprintf("its op %q is neither fetch nor push", b.Op)
	case b.Op == manager.GitOpFetch && len(b.Refs) > 0:
		return fmt.Sprintf("a fetch names no refs, and this request names %d", len(b.Refs))
	case len(b.Refs) > 1000:
		return fmt.Sprintf("it names %d refs, and a push may name at most 1000", len(b.Refs))
	case b.WaitSeconds != nil && *b.WaitSeconds < 0:
		return fmt.Sprintf("its waitSeconds is %d, and it must be 0 or more", *b.WaitSeconds)
	}
	for _, ref := range b.Refs {
		switch {
		case !gitRefName(ref.Ref):
			return fmt.Sprintf("the ref %q is not a full ref name under refs/ that git accepts", ref.Ref)
		case !gitObjectID(ref.Old):
			return fmt.Sprintf("the old id of %q is not lower-case hex of 40 or 64 characters", ref.Ref)
		case !gitObjectID(ref.New):
			return fmt.Sprintf("the new id of %q is not lower-case hex of 40 or 64 characters", ref.Ref)
		case len(ref.Old) != len(ref.New):
			return fmt.Sprintf("the old and new ids of %q differ in length", ref.Ref)
		}
	}
	return ""
}

// gitRefName reports whether s is a full ref name under refs/ that git's
// check-ref-format accepts: no component that starts with a dot or ends
// with .lock, no empty component, no "..", no "@{", no control character,
// space, ~, ^, :, ?, *, [ or backslash, and no trailing dot.
func gitRefName(s string) bool {
	rest, ok := strings.CutPrefix(s, "refs/")
	if !ok || rest == "" || strings.HasSuffix(s, ".") || strings.Contains(s, "..") || strings.Contains(s, "@{") {
		return false
	}
	for _, c := range s {
		if c < 0x20 || c == 0x7f || strings.ContainsRune(" ~^:?*[\\", c) {
			return false
		}
	}
	for _, part := range strings.Split(rest, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// gitObjectID reports whether s is a git object id: lower-case hex of 40
// characters for SHA-1 or 64 for SHA-256.
func gitObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// queried reports whether a git URL carries a query or a fragment. Both
// routes refuse such a URL before any lookup, with a sentence that quotes
// none of it, because a query can carry a token and every sentence reaches
// the person's terminal.
func queried(upstream string) bool {
	return strings.ContainsAny(upstream, "?#")
}

// resolveGit finds the one server that serves git for upstream: every
// server whose git upstream followed by a slash prefixes the URL, shows the
// subject its fetch or its push tool by tier one, as /mcp's visibility does,
// and holds owner/name, read from the next two path segments, in its object
// list without regard to case. It answers 200 with the repo, or 404 or 409
// with the sentence. A 404 asks every server it looked at for a fresh
// listing. In memory only.
func (a *App) resolveGit(sub policy.Subject, upstream string) (gitRepo, int, string) {
	tier1 := a.catalogFor(sub.Roles)
	var (
		found    []gitRepo
		names    []string
		missed   []string
		repoName string
	)
	for _, view := range a.manager.Views() {
		g := view.Manifest.Straza.Git
		if g == nil || !strings.HasPrefix(upstream, g.Upstream+"/") {
			continue
		}
		name, ok := repoPath(strings.TrimPrefix(upstream, g.Upstream+"/"))
		if !ok {
			continue
		}
		oc, ok := a.gitObjects(tier1, view.Name, g)
		if !ok {
			continue
		}
		names, missed, repoName = append(names, view.Name), append(missed, oc.AppID), name
		if list := a.objects.Get(oc.AppID); list != nil && list.Type == oc.Type {
			if o, ok := list.ByValueFold(name); ok {
				found = append(found, gitRepo{app: view.Name, git: *g, repo: o.Value, typ: oc.Type, id: o.ID})
			}
		}
	}
	switch {
	case len(found) == 1:
		return found[0], http.StatusOK, ""
	case len(found) > 1:
		return gitRepo{}, http.StatusConflict, fmt.Sprintf(grantAmbiguousMsg, found[0].app, found[1].app, found[0].repo)
	case len(names) == 0:
		return gitRepo{}, http.StatusNotFound, fmt.Sprintf(grantNoServerMsg, redact.URL(upstream))
	}
	for _, id := range missed {
		a.objects.Missed(id)
	}
	return gitRepo{}, http.StatusNotFound, fmt.Sprintf(grantNotListedMsg, repoName, strings.Join(names, " and "))
}

// gitObjects answers the objects declaration of the first tool of g's ops
// that the catalog shows on app, fetch before push, when that tool names
// its repo in an argument, and whether there is one.
func (a *App) gitObjects(tier1 *sessionCatalog, app string, g *manager.GitSpec) (manager.ObjectsCall, bool) {
	for _, op := range []string{manager.GitOpFetch, manager.GitOpPush} {
		tool := g.Tool(op)
		if t, ok := tier1.targets[app+"__"+tool]; tool == "" || !ok || t.app != app || t.tool != tool {
			continue
		}
		if oc, ok := a.manager.ObjectsFor(app, tool); ok && oc.Reach.Reach == manager.ReachArgument {
			return oc, true
		}
	}
	return manager.ObjectsCall{}, false
}

// repoPath reads owner/name from the path after a git upstream: two
// segments, a trailing slash allowed, and .git cut from the name.
func repoPath(rest string) (string, bool) {
	owner, name, ok := strings.Cut(strings.TrimSuffix(rest, "/"), "/")
	name = strings.TrimSuffix(name, ".git")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", false
	}
	return owner + "/" + name, true
}

// writeGitRefusal answers a resolve that did not find exactly one server:
// 404 with the code not_listed or 409 with the code ambiguous.
func writeGitRefusal(w http.ResponseWriter, status int, msg string) {
	code := "not_listed"
	if status == http.StatusConflict {
		code = "ambiguous"
	}
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

// grantArgs is the arguments the decision of a git grant runs with: the
// repo under the tool's pinned argument, and on a push with refs, branch,
// when exactly one ref is under refs/heads/, and refs. They feed the object
// gate, the approval's fingerprint and its card, and the members keep this
// order so the card reads the repo first.
func grantArgs(arg, repo string, refs []authn.GrantRef) json.RawMessage {
	var b bytes.Buffer
	member := func(sep, key string, value any) {
		k, _ := json.Marshal(key)
		v, _ := json.Marshal(value)
		b.WriteString(sep)
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	member("{", arg, repo)
	if len(refs) > 0 {
		var branches []string
		for _, r := range refs {
			if short, ok := strings.CutPrefix(r.Ref, "refs/heads/"); ok {
				branches = append(branches, short)
			}
		}
		if len(branches) == 1 {
			member(",", "branch", branches[0])
		}
		member(",", "refs", refs)
	}
	b.WriteByte('}')
	return b.Bytes()
}

// grantKind is the act.kind of a grant for a subject whose user type is
// userType and whom check-in found to be a person when person is set, by
// userKind's rule: agent or service when the type says so, agent for a
// non-person with no type, such as one marked only by attrs.kind nhi, and
// human for a person, who usually carries no type at all.
func grantKind(userType string, person bool) string {
	switch {
	case userType == store.UserTypeAgent || userType == store.UserTypeService:
		return userType
	case !person:
		return store.UserTypeAgent
	}
	return store.UserTypeHuman
}

// gitUpstreams answers, sorted and each once, the canonical upstream of
// every server whose manifest has a git block, for the check-in answer. It
// is the same for every subject, because it holds only host URLs and feeds
// one file for the whole machine. In memory only.
func (a *App) gitUpstreams() []string {
	var out []string
	for _, view := range a.manager.Views() {
		if g := view.Manifest.Straza.Git; g != nil {
			out = append(out, g.Upstream)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
