package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/redact"
	"github.com/strazahq/straza/internal/store"
)

// The sentences the drafting tools answer, pinned here word for word.
const (
	wantRoleRefusal   = "Straza: straza__draft_submit needs the Straza role straza-draft-config, and this session does not hold it. Ask an administrator to assign it in your identity manager."
	wantSubmitNext    = "Straza checks the draft now. Call straza__draft_status with draft %s to read the check. A person publishes it on the console or with strazactl, and you cannot."
	wantNotChecked    = "Straza has not checked revision %d yet. Call straza__draft_status again in a few seconds."
	wantFixRefused    = "Fix the refused lines and submit again with draft %s."
	wantStale         = "Draft %s went stale: live state changed under it. Ask a person to check it again on the console or with strazactl drafts rebase %s, or submit the documents as a new draft."
	wantWaits         = "Draft %s waits for a person to publish it on the console or with strazactl. You cannot publish it."
	wantPublished     = "A person published draft %s on %s."
	wantDiscarded     = "Draft %s was discarded: %s."
	wantExpired       = "Draft %s expired after 14 days without a change."
	wantNoOpenDraft   = "Straza: no open draft %s of yours. Submit without draft to start a new one."
	wantNoSuchDraft   = "Straza: no such draft %s"
	wantSubmitRate    = "Straza: straza__draft_submit takes one draft every 10 seconds for each agent. Wait, then submit again."
	wantSessionRate   = `Straza: rate limit exceeded for the MCP server "straza" (2 rps). Retry shortly`
	wantNotSaved      = "Straza: the draft was not saved. "
	wantSubmitShape   = "Straza: straza__draft_submit takes a JSON object with documents, a list of YAML texts, and optionally note and draft. Send the arguments in that shape."
	wantStatusShape   = "Straza: straza__draft_status takes a JSON object with draft, the id straza__draft_submit answered. Send the arguments in that shape."
	weatherURL        = "https://weather.example/mcp"
	missingServerRole = "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: nosuch-readers\nspec:\n    kind: application\n    bindings:\n        - app: nosuch\n          tools: [read]\n"
)

// doorRig is the drafts fixture with agents that hold straza-draft-config,
// each sponsored by kim, the fixture's root person.
type doorRig struct {
	*draftsFixture
}

// newDoorRig builds the drafts fixture for the drafting tools.
func newDoorRig(t *testing.T, mutators ...func(*config.Config)) *doorRig {
	t.Helper()
	return &doorRig{newDraftsFixture(t, nil, mutators...)}
}

// agent seeds the agent name, sponsored by kim and holding
// straza-draft-config, checks it in on the gateway as claude-code, and
// answers the user and the session token. Each agent has its own
// one-draft-in-10-seconds budget, so every case submits as its own agent.
func (r *doorRig) agent(t *testing.T, name string) (store.User, string) {
	t.Helper()
	u := seedAgent(t, r.app, name, "kim", DraftConfigRole)
	return u, sessionToken(t, r.base, name)
}

// callTool calls the tool name through the gateway as the session tok and
// answers the text of the result, whether it is an error, and its
// structured content. A JSON-RPC error answers its message as the text.
func callTool(t *testing.T, base, tok, name string, args any) (string, bool, map[string]any) {
	t.Helper()
	_, res, raw := mcpCall(t, base, tok, "tools/call", map[string]any{"name": name, "arguments": args})
	if e, _ := res["error"].(map[string]any); e != nil {
		msg, _ := e["message"].(string)
		return msg, true, nil
	}
	result, _ := res["result"].(map[string]any)
	if result == nil {
		t.Fatalf("%s answered no result: %s", name, raw)
	}
	isErr, _ := result["isError"].(bool)
	sc, _ := result["structuredContent"].(map[string]any)
	text := ""
	if content, _ := result["content"].([]any); len(content) > 0 {
		if c, _ := content[0].(map[string]any); c != nil {
			text, _ = c["text"].(string)
		}
	}
	return text, isErr, sc
}

// submit calls straza__draft_submit with documents and answers the result.
func submit(t *testing.T, base, tok string, args map[string]any) (string, bool, map[string]any) {
	t.Helper()
	return callTool(t, base, tok, "straza__draft_submit", args)
}

// status calls straza__draft_status for the draft id.
func status(t *testing.T, base, tok, id string) (string, bool, map[string]any) {
	t.Helper()
	return callTool(t, base, tok, "straza__draft_status", map[string]any{"draft": id})
}

// agentActor is the actor a draft of the straza-app door records for u.
func (r *doorRig) agentActor(u store.User) store.DraftActor {
	return store.DraftActor{ID: u.ID, Name: u.Username, Agent: true, Via: laneSession, Client: "claude-code",
		SponsorID: r.kim.ID, SponsorName: "kim"}
}

// storeDraft stores an unchecked draft of door with the documents, written
// by author, expiring at expires, and answers its row.
func (r *doorRig) storeDraft(t *testing.T, door string, author store.DraftActor, expires time.Time, documents ...string) store.DraftRow {
	t.Helper()
	items, fs := drafts.ParseBundle(documents)
	if len(fs) > 0 {
		t.Fatalf("the documents do not read: %+v", fs)
	}
	row, err := r.app.store.Drafts().Create(context.Background(), store.DraftRow{Door: door, ExpiresAt: &expires},
		rowsOf(items), store.DraftRevisionRow{Author: author, Digest: revisionDigest(items)})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

// awaitChecked waits until the draft id is checked at its current revision
// and answers its row. The 5-second bound sits far below the checker's
// 30-second tick, so it holds only when a submit wakes the checker.
func (r *doorRig) awaitChecked(t *testing.T, id string) store.DraftRow {
	t.Helper()
	n, _ := strconv.ParseInt(id, 10, 64)
	deadline := time.Now().Add(5 * time.Second)
	for {
		row, _, err := r.app.store.Drafts().Get(context.Background(), n)
		if err != nil {
			t.Fatal(err)
		}
		if row.CheckedRevision == row.Revision {
			return row
		}
		if time.Now().After(deadline) {
			t.Fatalf("draft %s is not checked 5 seconds after its submit", id)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// proposed lists the drafts whose proposer is u.
func (r *doorRig) proposed(t *testing.T, u store.User) []store.DraftRow {
	t.Helper()
	rows, err := r.app.store.Drafts().List(context.Background(), store.DraftFilter{ProposerID: u.ID}, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestDraftingToolsListedOnlyToHolders pins that tier one lists the two
// drafting tools only for a role set holding straza-draft-config, the
// catalogs of holders and others never share a key, and the gateway lists
// them to a holder and never to anyone else.
func TestDraftingToolsListedOnlyToHolders(t *testing.T) {
	t.Parallel()
	r := newDoorRig(t)
	drafting := []string{"straza__draft_status", "straza__draft_submit"}
	names := func(c *sessionCatalog) []string {
		var out []string
		for _, tl := range c.tools {
			if slices.Contains(drafting, tl.Name) {
				out = append(out, tl.Name)
			}
		}
		return out
	}
	if got := names(r.app.buildCatalog([]string{"dev"})); len(got) != 0 {
		t.Errorf("tier one without the role lists %v", got)
	}
	holder := r.app.buildCatalog([]string{DraftConfigRole, "dev"})
	if got := names(holder); !slices.Equal(got, drafting) {
		t.Errorf("tier one of a holder lists %v, want %v", got, drafting)
	}
	for _, n := range drafting {
		if tgt := holder.targets[n]; tgt.app != nativeAppName || tgt.bindingID != "" {
			t.Errorf("the target of %s is %+v, want the built-in app with no access row", n, tgt)
		}
	}
	if a, b := r.app.catalogFor([]string{"dev"}), r.app.catalogFor([]string{"dev", DraftConfigRole}); a.key == b.key {
		t.Errorf("a holder and a non-holder share the catalog key %q", a.key)
	}

	_, joeTok := r.agent(t, "joe")
	listed := toolNamesOf(t, second(mcpCall(t, r.base, joeTok, "tools/list", nil)))
	for _, n := range drafting {
		if !slices.Contains(listed, n) {
			t.Errorf("joe's tools/list lacks %s: %v", n, listed)
		}
	}
	botTok := sessionToken(t, r.base, "bot")
	for _, n := range toolNamesOf(t, second(mcpCall(t, r.base, botTok, "tools/list", nil))) {
		if slices.Contains(drafting, n) {
			t.Errorf("bot, which lacks the role, is listed %s", n)
		}
	}
}

// TestDraftingToolsCarryTheGrantedFact pins the one granted-fact
// function on its three callers: the gateway call, the overlay probe and
// simulate's access check.
func TestDraftingToolsCarryTheGrantedFact(t *testing.T) {
	t.Parallel()
	r := newDoorRig(t)
	joe, joeTok := r.agent(t, "joe")
	text, isErr, _ := status(t, r.base, joeTok, "999")
	if !isErr || text != fmt.Sprintf(wantNoSuchDraft, "999") {
		t.Errorf("status of an unknown draft = %q (error %v)", text, isErr)
	}
	rec := awaitMCPRecords(t, r.app, 1, func(d map[string]any) bool { return d["user"] == joe.ID && d["toolName"] == "draft_status" })[0]
	for k, v := range map[string]any{"effect": "allow", "granted": true, "default": true, "ruleId": "",
		"reason": "allowed by role access. No policy rule gates this tool."} {
		if rec[k] != v {
			t.Errorf("the gateway record's %s = %v, want %v", k, rec[k], v)
		}
	}

	denyJoe, err := policy.Parse([]byte("apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata: {name: no-joe}\nspec:\n  match: {users: [joe]}\n  rules:\n" +
		"    - id: no-submit\n      tools: [mcp.call]\n      apps: [straza]\n      toolNames: {deny: [draft_submit]}\n      effect: deny\n"))
	if err != nil {
		t.Fatal(err)
	}
	holder := policy.Subject{User: "joe", Roles: []string{DraftConfigRole}}
	cases := []struct {
		name    string
		filter  bool
		docs    []policy.Document
		visible []string
	}{
		{"no rule, filter off", false, nil, []string{"straza__draft_status", "straza__draft_submit"}},
		{"no rule, filter on", true, nil, []string{"straza__draft_status", "straza__draft_submit"}},
		{"a deny, filter off, lists it as a proxied tool is listed", false, []policy.Document{denyJoe}, []string{"straza__draft_status", "straza__draft_submit"}},
		{"a deny, filter on, hides it", true, []policy.Document{denyJoe}, []string{"straza__draft_status"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, err := policy.NewEngine(tc.docs, policy.EffectAllow)
			if err != nil {
				t.Fatal(err)
			}
			a := &App{cfg: config.Config{Apps: config.Apps{Catalog: config.Catalog{PolicyFilter: tc.filter}}}}
			tier1 := &sessionCatalog{targets: map[string]gwTarget{}}
			a.appendNativeCatalog(tier1, holder.Roles)
			var got []string
			for _, tl := range overlayTools(tier1, a.buildOverlay("k", holder, tier1, eng)) {
				got = append(got, tl.Name)
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.visible) {
				t.Errorf("the overlay shows %v, want %v (the approval tools stay hidden without a rule)", got, tc.visible)
			}
		})
	}

	for _, tc := range []struct {
		roles []string
		tool  string
		want  bool
	}{
		{[]string{DraftConfigRole}, "draft_submit", true},
		{[]string{DraftConfigRole}, "draft_status", true},
		{[]string{DraftConfigRole}, "approval_status", false},
		{[]string{"dev"}, "draft_submit", false},
	} {
		if got := r.app.hasAccess(tc.roles, nativeAppName, tc.tool); got != tc.want {
			t.Errorf("hasAccess(%v, straza, %s) = %v, want %v", tc.roles, tc.tool, got, tc.want)
		}
	}
	var sim simResult
	req := map[string]any{"event": map[string]any{"kind": "tool.pre", "tool": "mcp.call", "app": "straza", "toolName": "draft_submit"},
		"subject": map[string]any{"roles": []string{DraftConfigRole}}}
	if code := adminReq(t, http.MethodPost, r.base+"/v1/admin/policies/simulate", r.root, req, &sim); code != http.StatusOK || sim.Active.Effect != "allow" {
		t.Errorf("simulate of a holder's draft_submit = %d %+v, want allow", code, sim.Active)
	}
}

// TestDraftingToolRefusedWithoutTheRole pins the two role refusals: a
// session without the role meets today's unknown tool, and a cached
// subject that lost the role between the catalog and the call meets the
// role sentence, recorded as a refusal after the allow, with nothing
// stored.
func TestDraftingToolRefusedWithoutTheRole(t *testing.T) {
	t.Parallel()
	r := newDoorRig(t)
	botTok := sessionToken(t, r.base, "bot")
	text, isErr, _ := submit(t, r.base, botTok, map[string]any{"documents": []string{draftApp("weather", weatherURL, "The weather.")}})
	if !isErr || text != `unknown tool "straza__draft_submit"` {
		t.Errorf("bot's submit = %q, want the unknown tool refusal", text)
	}

	joe, joeTok := r.agent(t, "joe")
	claims, err := r.app.tokens.Verify(joeTok)
	if err != nil {
		t.Fatal(err)
	}
	r.app.subjects.put(claims.Session, policy.Subject{User: "joe", Roles: []string{}}, time.Time{})
	params, _ := json.Marshal(map[string]any{"name": "straza__draft_submit",
		"arguments": map[string]any{"documents": []string{draftApp("weather", weatherURL, "The weather.")}}})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r.app.handleToolCall(w, req, rpcRequest{ID: json.RawMessage("1"), Params: params}, claims,
		policy.Subject{User: "joe", Roles: []string{DraftConfigRole}})
	if !strings.Contains(w.Body.String(), wantRoleRefusal) || !strings.Contains(w.Body.String(), `"isError":true`) {
		t.Errorf("the call without the cached role answered %s", w.Body.String())
	}
	rec := awaitMCPRecords(t, r.app, 1, func(d map[string]any) bool { return d["user"] == joe.ID && d["toolName"] == "draft_submit" })[0]
	if rec["effect"] != "deny" || rec["reason"] != wantRoleRefusal {
		t.Errorf("the record = %v, want a deny with the role sentence", rec)
	}
	for _, tool := range []string{nativeToolDraftSubmit, nativeToolDraftStatus} {
		res := r.app.callNativeStraza(context.Background(), tool, json.RawMessage(`{"documents":["x"],"draft":"1"}`), claims,
			policy.Subject{User: "joe", Roles: []string{DraftConfigRole}})
		want := strings.Replace(wantRoleRefusal, "straza__draft_submit", "straza__"+tool, 1)
		if text := res.Content[0].(*mcp.TextContent).Text; !res.IsError || text != want {
			t.Errorf("the handler of %s without the cached role answered %q", tool, text)
		}
	}
	if got := r.proposed(t, joe); len(got) != 0 {
		t.Errorf("the refused call stored %d drafts", len(got))
	}
}

// TestDraftSubmitAndStatus pins the submit and status through the gateway:
// what a submit stores, records and answers, and each of the six next sentences of
// status.
func TestDraftSubmitAndStatus(t *testing.T) {
	t.Parallel()
	r := newDoorRig(t)
	ctx := context.Background()

	// Not checked comes first, before any submit wakes the checker, so no
	// sweep reaches the draft the store made.
	idle, idleTok := r.agent(t, "idle")
	row := r.storeDraft(t, "straza-app", r.agentActor(idle), time.Now().Add(time.Hour), draftApp("weather", weatherURL, "The weather."))
	id := strconv.FormatInt(row.ID, 10)
	text, isErr, sc := status(t, r.base, idleTok, id)
	if isErr || sc["next"] != fmt.Sprintf(wantNotChecked, 1) || sc["checked"] != false || sc["publishable"] != false || sc["state"] != "open" {
		t.Errorf("status of an unchecked draft = %q %v", text, sc)
	}

	pub, pubTok := r.agent(t, "pub")
	text, isErr, sc = submit(t, r.base, pubTok, map[string]any{"documents": []string{draftApp("weather", weatherURL, "The weather.")},
		"note": "Weather for the travel team."})
	if isErr {
		t.Fatalf("submit = %q", text)
	}
	pubID, _ := sc["draft"].(string)
	want := map[string]any{"revision": float64(1), "state": "open", "checked": false, "publishable": false, "next": fmt.Sprintf(wantSubmitNext, pubID)}
	for k, v := range want {
		if sc[k] != v {
			t.Errorf("the submit answer's %s = %v, want %v", k, sc[k], v)
		}
	}
	stored := r.awaitChecked(t, pubID)
	if stored.Door != "straza-app" || stored.Proposer != r.agentActor(pub) || stored.Note != "Weather for the travel team." {
		t.Errorf("the stored draft = door %q, proposer %+v, note %q", stored.Door, stored.Proposer, stored.Note)
	}
	if stored.ExpiresAt == nil || stored.ExpiresAt.Sub(time.Now().Add(draftExpiry)).Abs() > time.Minute {
		t.Errorf("the draft expires at %v, want 14 days from now", stored.ExpiresAt)
	}
	recs := draftRecords(t, r.app, "draft.create", pubID)
	if len(recs) != 1 {
		t.Fatalf("%d draft.create records, want 1", len(recs))
	}
	for k, v := range map[string]any{"actor": "pub", "actorId": pub.ID, "actorVia": "session", "client": "claude-code",
		"sponsor": "kim", "sponsorId": r.kim.ID, "door": "straza-app", "revision": float64(1)} {
		if recs[0][k] != v {
			t.Errorf("draft.create's %s = %v, want %v", k, recs[0][k], v)
		}
	}
	_, isErr, sc = status(t, r.base, pubTok, pubID)
	if isErr || sc["checked"] != true || sc["publishable"] != true || sc["next"] != fmt.Sprintf(wantWaits, pubID) {
		t.Errorf("status of a publishable draft = %v", sc)
	}
	for _, list := range []string{"refused", "risks", "warnings", "unchecked"} {
		items, ok := sc[list].([]any)
		if !ok {
			t.Errorf("status lacks the list %s: %v", list, sc)
		}
		for _, f := range items {
			if key, _ := f.(map[string]any)["key"].(string); len(key) != 64 {
				t.Errorf("a finding of %s has no key: %v", list, f)
			}
		}
	}

	_, badTok := r.agent(t, "bad")
	_, isErr, sc = submit(t, r.base, badTok, map[string]any{"documents": []string{missingServerRole}})
	if isErr {
		t.Fatalf("the submit of a draft the check refuses was refused at intake: %v", sc)
	}
	badID, _ := sc["draft"].(string)
	r.awaitChecked(t, badID)
	_, _, sc = status(t, r.base, badTok, badID)
	if refused, _ := sc["refused"].([]any); len(refused) == 0 || sc["publishable"] != false || sc["next"] != fmt.Sprintf(wantFixRefused, badID) {
		t.Errorf("status of a refused draft = %v", sc)
	}

	if code, a := r.publishAll(t, r.root, pubID); code != http.StatusOK {
		t.Fatalf("kim's publish of %s = %d %q", pubID, code, a.Error)
	}
	done, _, err := r.app.store.Drafts().Get(ctx, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second) // the session rate, 2 calls a second
	_, _, sc = status(t, r.base, pubTok, pubID)
	if sc["state"] != "published" || sc["publishable"] != false || sc["next"] != fmt.Sprintf(wantPublished, pubID, done.DecidedAt.UTC().Format(time.RFC3339)) {
		t.Errorf("status of a published draft = %v", sc)
	}

	cur, _ := r.stored(t, badID)
	if code, _ := r.call(t, http.MethodPost, "/v1/admin/drafts/"+badID+"/discard", r.root, map[string]any{"revision": cur.Revision, "reason": "not needed"}); code != http.StatusOK {
		t.Fatalf("kim's discard of %s = %d", badID, code)
	}
	time.Sleep(time.Second)
	if _, _, sc = status(t, r.base, badTok, badID); sc["state"] != "discarded" || sc["next"] != fmt.Sprintf(wantDiscarded, badID, "not needed") {
		t.Errorf("status of a discarded draft = %v", sc)
	}

	old, oldTok := r.agent(t, "old")
	gone := r.storeDraft(t, "straza-app", r.agentActor(old), time.Now().Add(-time.Minute), draftApp("weather", weatherURL, "The weather."))
	if err := r.app.checkDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	goneID := strconv.FormatInt(gone.ID, 10)
	if _, _, sc = status(t, r.base, oldTok, goneID); sc["state"] != "expired" || sc["next"] != fmt.Sprintf(wantExpired, goneID) {
		t.Errorf("status of an expired draft = %v", sc)
	}
	if text, isErr, _ := status(t, r.base, oldTok, pubID); !isErr || text != fmt.Sprintf(wantNoSuchDraft, pubID) {
		t.Errorf("status of another proposer's draft = %q", text)
	}
	time.Sleep(time.Second)
	apiDraft := strconv.FormatInt(r.storeDraft(t, "api", r.agentActor(old), time.Now().Add(time.Hour), draftApp("weather", weatherURL, "The weather.")).ID, 10)
	if text, isErr, _ := status(t, r.base, oldTok, apiDraft); !isErr || text != fmt.Sprintf(wantNoSuchDraft, apiDraft) {
		t.Errorf("status of the caller's own draft of another door = %q", text)
	}

	// A revision keeps the base its object was stamped with, so a draft
	// whose only refusal is draft.stale names the way out instead of asking
	// for a revision that would meet the same refusal.
	st, stTok := r.agent(t, "stale")
	github := draftApp("github", "https://api.github.example/mcp?region=eu", "The GitHub server, read only.")
	stRow := r.storeDraft(t, "straza-app", r.agentActor(st), time.Now().Add(time.Hour), github)
	if err := r.app.checkDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	putServer(t, r.app, draftApp("github", "https://api.github.example/mcp?region=us", "The GitHub server."))
	stID := strconv.FormatInt(stRow.ID, 10)
	if text, isErr, _ := submit(t, r.base, stTok, map[string]any{"documents": []string{github}, "draft": stID}); isErr {
		t.Fatalf("the revision of the stale draft = %q", text)
	}
	r.awaitChecked(t, stID)
	_, _, sc = status(t, r.base, stTok, stID)
	refused, _ := sc["refused"].([]any)
	for _, f := range refused {
		if code := f.(map[string]any)["code"]; code != "draft.stale" {
			t.Errorf("the stale draft is refused with %v", code)
		}
	}
	if len(refused) == 0 || sc["next"] != fmt.Sprintf(wantStale, stID, stID) {
		t.Errorf("status of a stale draft = %v", sc)
	}
}

// TestDraftSubmitRevisesOwnDraft pins that draft names the caller's own
// open draft of the straza-app door, and that an agent revises only a
// draft whose every revision it wrote.
func TestDraftSubmitRevisesOwnDraft(t *testing.T) {
	t.Parallel()
	r := newDoorRig(t)
	ctx := context.Background()
	later := time.Now().Add(time.Hour)
	doc := draftApp("weather", weatherURL, "The weather.")

	rev, revTok := r.agent(t, "rev")
	own := r.storeDraft(t, "straza-app", r.agentActor(rev), later, doc)
	ownID := strconv.FormatInt(own.ID, 10)
	text, isErr, sc := submit(t, r.base, revTok, map[string]any{"documents": []string{draftApp("weather", weatherURL, "The weather, revised.")}, "draft": ownID})
	if isErr || sc["draft"] != ownID || sc["revision"] != float64(2) {
		t.Fatalf("the revision = %q %v", text, sc)
	}
	got, items := r.stored(t, ownID)
	if got.Revision != 2 || len(items) != 1 || !strings.Contains(items[0].Doc, "revised") || got.ExpiresAt.Sub(time.Now().Add(draftExpiry)).Abs() > time.Minute {
		t.Errorf("the draft after the revision = %+v %+v", got, items)
	}
	if recs := draftRecords(t, r.app, "draft.update", ownID); len(recs) != 1 || recs[0]["revision"] != float64(2) || recs[0]["actor"] != "rev" {
		t.Errorf("draft.update records = %v", recs)
	}

	other, _ := r.agent(t, "other")
	closed := func(u store.User) store.DraftRow {
		row := r.storeDraft(t, "straza-app", r.agentActor(u), later, doc)
		if _, err := r.app.store.Drafts().Close(ctx, row.ID, 0, "discarded", store.DraftActor{}, "", time.Now()); err != nil {
			t.Fatal(err)
		}
		return row
	}
	personRevised := func(u store.User) store.DraftRow {
		row := r.storeDraft(t, "straza-app", r.agentActor(u), later, doc)
		items, _ := drafts.ParseBundle([]string{doc})
		if _, err := r.app.store.Drafts().Revise(ctx, row.ID, store.DraftRevise{From: 1, Items: rowsOf(items),
			Rev: store.DraftRevisionRow{Author: store.DraftActor{ID: r.kim.ID, Name: "kim", Via: laneLogin, Client: clientLogin}, Door: "api"}}); err != nil {
			t.Fatal(err)
		}
		return row
	}
	cases := []struct {
		name  string
		draft func(caller store.User) string
	}{
		{"another agent's draft", func(store.User) string {
			return strconv.FormatInt(r.storeDraft(t, "straza-app", r.agentActor(other), later, doc).ID, 10)
		}},
		{"a closed draft", func(u store.User) string { return strconv.FormatInt(closed(u).ID, 10) }},
		{"its own draft of another door", func(u store.User) string {
			return strconv.FormatInt(r.storeDraft(t, "api", r.agentActor(u), later, doc).ID, 10)
		}},
		{"a draft a person revised", func(u store.User) string { return strconv.FormatInt(personRevised(u).ID, 10) }},
		{"an id that is no number", func(store.User) string { return "abc" }},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caller, tok := r.agent(t, fmt.Sprintf("caller%d", i))
			id := tc.draft(caller)
			var before store.DraftRow
			if n, err := strconv.ParseInt(id, 10, 64); err == nil {
				before, _, _ = r.app.store.Drafts().Get(ctx, n)
			}
			text, isErr, _ := submit(t, r.base, tok, map[string]any{"documents": []string{doc}, "draft": id})
			if !isErr || text != fmt.Sprintf(wantNoOpenDraft, id) {
				t.Errorf("the revision = %q, want the no-open-draft sentence", text)
			}
			if n, err := strconv.ParseInt(id, 10, 64); err == nil {
				if after, _, _ := r.app.store.Drafts().Get(ctx, n); after.Revision != before.Revision || after.State != before.State {
					t.Errorf("the refused revision moved draft %s from %d %s to %d %s", id, before.Revision, before.State, after.Revision, after.State)
				}
			}
		})
	}
}

// TestDraftSubmitRefusalStoresNothing pins that a refusal that needs no
// live state answers one tool error, one clause per finding, and stores
// nothing and records no draft.
func TestDraftSubmitRefusalStoresNothing(t *testing.T) {
	t.Parallel()
	r := newDoorRig(t)
	runner := "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: runner\nserver:\n  name: io.x/runner\n  version: 1.0.0\n" +
		"straza:\n  runtime:\n    kind: command\n    command:\n      exec: /usr/bin/runner\n"
	cases := []struct {
		name  string
		setup func(t *testing.T) (store.User, string)
		args  map[string]any
		want  string
	}{
		{"a command server", func(t *testing.T) (store.User, string) { return r.agent(t, "cmd") },
			map[string]any{"documents": []string{runner}},
			wantNotSaved + "App/runner: An agent cannot propose a command or container server, because such a server runs code on the Straza host as Straza's own user, with its data directory in reach. " +
				"Propose a remote server, or ask a holder of straza-global-mcp-admin to add this one."},
		{"no document", func(t *testing.T) (store.User, string) { return r.agent(t, "empty") },
			map[string]any{"documents": []string{}},
			wantNotSaved + "The draft holds no document. Add at least one App, Role, PolicySet or Removal document."},
		{"an agent with no sponsor", func(t *testing.T) (store.User, string) {
			u := seedAgent(t, r.app, "lone", "", DraftConfigRole)
			return u, sessionToken(t, r.base, "lone")
		}, map[string]any{"documents": []string{draftApp("weather", weatherURL, "The weather.")}},
			wantNotSaved + "lone has no active sponsor, and an agent's draft needs a person who answers for it. Ask an administrator to set its sponsor in your identity manager, then submit again."},
		{"an eleventh open draft", func(t *testing.T) (store.User, string) {
			u, tok := r.agent(t, "busy")
			for range 10 {
				r.storeDraft(t, "straza-app", r.agentActor(u), time.Now().Add(time.Hour), draftApp("weather", weatherURL, "The weather."))
			}
			return u, tok
		}, map[string]any{"documents": []string{draftApp("weather", weatherURL, "The weather.")}},
			wantNotSaved + "busy already has 10 open drafts, and a proposer may keep at most 10. Publish, discard or wait for one of them, then send this draft again."},
		{"arguments of another shape", func(t *testing.T) (store.User, string) { return r.agent(t, "shape") },
			map[string]any{"documents": "not a list"}, wantSubmitShape},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, tok := tc.setup(t)
			before := len(r.proposed(t, u))
			text, isErr, _ := submit(t, r.base, tok, tc.args)
			if !isErr || text != tc.want {
				t.Errorf("submit = %q\nwant       %q", text, tc.want)
			}
			if after := len(r.proposed(t, u)); after != before {
				t.Errorf("the refusal stored %d drafts", after-before)
			}
			for _, ev := range adminAuditEvents(t, r.app) {
				if ev["actorId"] == u.ID && strings.HasPrefix(fmt.Sprint(ev["action"]), "draft.") {
					t.Errorf("the refusal recorded %v", ev)
				}
			}
		})
	}
}

// TestDraftingRates pins the drafting rates: one submit every 10 seconds for
// each user across sessions, two drafting calls a second for each session, and no
// new limit on the approval tools.
func TestDraftingRates(t *testing.T) {
	t.Parallel()
	r := newDoorRig(t)
	ctx := context.Background()
	doc := map[string]any{"documents": []string{draftApp("weather", weatherURL, "The weather.")}}

	fast, fastTok := r.agent(t, "fast")
	if text, isErr, _ := submit(t, r.base, fastTok, doc); isErr {
		t.Fatalf("the first submit = %q", text)
	}
	if text, isErr, _ := submit(t, r.base, fastTok, doc); !isErr || text != wantSubmitRate {
		t.Errorf("a second submit within 10 seconds = %q", text)
	}
	if text, isErr, _ := submit(t, r.base, sessionToken(t, r.base, "fast"), doc); !isErr || text != wantSubmitRate {
		t.Errorf("a submit from fast's second session = %q", text)
	}
	recs := awaitMCPRecords(t, r.app, 2, func(d map[string]any) bool { return d["user"] == fast.ID && d["reason"] == wantSubmitRate })
	if recs[0]["effect"] != "deny" || recs[0]["ruleId"] != "" {
		t.Errorf("the rate refusal's record = %v", recs[0])
	}
	_, slowTok := r.agent(t, "slow")
	if text, isErr, _ := submit(t, r.base, slowTok, doc); isErr {
		t.Errorf("another agent's submit = %q", text)
	}

	_, pollTok := r.agent(t, "poll")
	limited := false
	for range 5 {
		if text, _, _ := status(t, r.base, pollTok, "1"); text == wantSessionRate {
			limited = true
		}
	}
	if !limited {
		t.Error("five status calls in a row met no session rate")
	}
	if _, err := r.app.store.Policies().Create(ctx, store.PolicySet{Name: "poll-approvals", Status: "active", YAMLSource: "apiVersion: straza.dev/v1beta1\nkind: PolicySet\n" +
		"metadata: {name: poll-approvals}\nspec:\n  match: {users: [poll]}\n  rules:\n    - id: approvals\n      tools: [mcp.call]\n      apps: [straza]\n" +
		"      toolNames: {allow: [approval_status]}\n      effect: allow\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.app.snapshots.Recompile(ctx); err != nil {
		t.Fatal(err)
	}
	pollTok = sessionToken(t, r.base, "poll")
	for i := range 6 {
		if text, _, _ := callTool(t, r.base, pollTok, "straza__approval_status", map[string]any{"ref": "nope"}); strings.Contains(text, "rate limit") {
			t.Errorf("approval_status call %d met a rate: %q", i+1, text)
		}
	}
}

// TestDraftSubmitRecordCarriesTheDigest pins the submit record: the
// straza.audit.mcp record of draft_submit holds the digest and the size of
// the arguments and never the arguments, so a secret the handler then
// refuses reaches no record, while draft_status keeps its arguments.
func TestDraftSubmitRecordCarriesTheDigest(t *testing.T) {
	t.Parallel()
	r := newDoorRig(t)
	const secret = "hunter2hunter2hunter2"
	leaky, tok := r.agent(t, "leaky")
	args := map[string]any{"documents": []string{draftApp("leaky", "https://svc:"+secret+"@leaky.example/mcp", "Leaky.")}}
	if text, isErr, _ := submit(t, r.base, tok, args); !isErr || !strings.HasPrefix(text, wantNotSaved+"App/leaky: ") || strings.Contains(text, secret) {
		t.Errorf("the submit of a secret = %q", text)
	}
	raw, _ := json.Marshal(args)
	sum := sha256.Sum256(raw)
	rec := awaitMCPRecords(t, r.app, 1, func(d map[string]any) bool { return d["user"] == leaky.ID && d["toolName"] == "draft_submit" })[0]
	if rec["argumentsDigest"] != hex.EncodeToString(sum[:]) || rec["argumentsSize"] != float64(len(raw)) {
		t.Errorf("the record = %v, want the digest %s and the size %d", rec, hex.EncodeToString(sum[:]), len(raw))
	}
	if _, ok := rec["arguments"]; ok {
		t.Errorf("the draft_submit record carries its arguments: %v", rec)
	}
	status(t, r.base, tok, "7")
	st := awaitMCPRecords(t, r.app, 1, func(d map[string]any) bool { return d["user"] == leaky.ID && d["toolName"] == "draft_status" })[0]
	if st["arguments"] != `{"draft":"7"}` {
		t.Errorf("the draft_status record's arguments = %v", st["arguments"])
	}
	rows, err := r.app.store.Outbox().ListRecent(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if strings.Contains(row.CE, secret) {
			t.Errorf("the outbox holds the secret: %s", row.CE)
		}
	}
}

// TestDraftSubmitApprovalPreviewIsTheDigest pins the digest preview on
// approvals: a hold, a ticket and an approval_request of draft_submit store
// the digest and the size as the preview and never the documents.
func TestDraftSubmitApprovalPreviewIsTheDigest(t *testing.T) {
	t.Parallel()
	r := newDoorRig(t, func(c *config.Config) { c.Approval.Preview.Enabled = true })
	ctx := context.Background()
	set := func(name, user, gate string) string {
		return "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata: {name: " + name + "}\nspec:\n  match: {users: [" + user + "]}\n  rules:\n" +
			"    - id: gate\n      tools: [mcp.call]\n      apps: [straza]\n      toolNames: {allow: [draft_submit]}\n      effect: allow\n      mode: approve\n      approve: " + gate + "\n" +
			"    - id: ask\n      tools: [mcp.call]\n      apps: [straza]\n      toolNames: {allow: [approval_request]}\n      effect: allow\n"
	}
	for name, src := range map[string]string{
		"held":   set("held", "held", "{roles: [sec], timeoutSeconds: 1}"),
		"ticket": set("ticket", "ticket", "{class: ticket, roles: [sec], ticketTTLSeconds: 86400, grantTTLSeconds: 3600}"),
		"asker":  set("asker", "asker", "{class: ticket, roles: [sec], ticketTTLSeconds: 86400, grantTTLSeconds: 3600}"),
	} {
		if _, err := r.app.store.Policies().Create(ctx, store.PolicySet{Name: name, Status: "active", YAMLSource: src}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.app.snapshots.Recompile(ctx); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"documents": []string{draftApp("weather", weatherURL, "The weather.")}}
	raw, _ := json.Marshal(args)
	sum := sha256.Sum256(raw)
	for _, name := range []string{"held", "ticket"} {
		_, tok := r.agent(t, name)
		submit(t, r.base, tok, args)
	}
	_, askTok := r.agent(t, "asker")
	if text, isErr, _ := callTool(t, r.base, askTok, "straza__approval_request", map[string]any{
		"action": map[string]any{"tool": "mcp.call", "app": "straza", "tool_name": "draft_submit", "args": args}}); isErr {
		t.Fatalf("approval_request = %q", text)
	}
	recs, err := r.app.approval.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, rec := range recs {
		seen[rec.Username] = true
		if !strings.Contains(rec.ArgsPreview, hex.EncodeToString(sum[:])) || !strings.Contains(rec.ArgsPreview, strconv.Itoa(len(raw))) || strings.Contains(rec.ArgsPreview, "weather.example") {
			t.Errorf("the approval of %s previews %q, want the digest and the size only", rec.Username, rec.ArgsPreview)
		}
	}
	for _, name := range []string{"held", "ticket", "asker"} {
		if !seen[name] {
			t.Errorf("no approval of %s was stored", name)
		}
	}
}

// TestSubjectCacheKeepsTheSessionFacts pins what check-in records beside
// the subject for the drafting tools: whether the user is a person and
// the harness name alone.
func TestSubjectCacheKeepsTheSessionFacts(t *testing.T) {
	t.Parallel()
	r := newDoorRig(t)
	_, joeTok := r.agent(t, "joe")
	for _, tc := range []struct {
		tok    string
		person bool
	}{{sessionToken(t, r.base, "kim"), true}, {joeTok, false}} {
		claims, err := r.app.tokens.Verify(tc.tok)
		if err != nil {
			t.Fatal(err)
		}
		e, ok := r.app.subjects.lookup(claims.Session)
		if !ok || e.facts != (sessionFacts{person: tc.person, harness: "claude-code"}) {
			t.Errorf("the cached facts of %s = %+v (%v), want person %v and harness claude-code", e.sub.User, e.facts, ok, tc.person)
		}
	}
}

// TestDraftSubmitByAPerson pins that a person who holds
// straza-draft-config submits as the agent door, so intake runs the agent
// rules but not the sponsor rule, and the check stores the verdict an
// agent reads, which status answers.
func TestDraftSubmitByAPerson(t *testing.T) {
	t.Parallel()
	r := newDoorRig(t)
	mkHuman(t, r.app, "pat", DraftConfigRole)
	mkHuman(t, r.app, "sam", DraftConfigRole)
	patTok, samTok := sessionToken(t, r.base, "pat"), sessionToken(t, r.base, "sam")

	_, isErr, sc := submit(t, r.base, patTok, map[string]any{"documents": []string{draftApp("weather", weatherURL, "The weather.")}})
	if isErr {
		t.Fatalf("a person's submit = %v", sc)
	}
	id, _ := sc["draft"].(string)
	row := r.awaitChecked(t, id)
	if row.Proposer.Agent || row.AgentVerdict == "" {
		t.Errorf("a person's draft of the door = proposer %+v, stored agent verdict %q", row.Proposer, row.AgentVerdict)
	}
	if _, isErr, sc = status(t, r.base, patTok, id); isErr || sc["checked"] != true || sc["next"] != fmt.Sprintf(wantWaits, id) {
		t.Errorf("status of a person's draft = %v", sc)
	}

	runner := "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: runner\nserver:\n  name: io.x/runner\n  version: 1.0.0\n" +
		"straza:\n  runtime:\n    kind: command\n    command:\n      exec: /usr/bin/runner\n"
	text, isErr, _ := submit(t, r.base, samTok, map[string]any{"documents": []string{runner}})
	if !isErr || !strings.HasPrefix(text, wantNotSaved+"App/runner: An agent cannot propose a command or container server") {
		t.Errorf("a person's command server through the door = %q, want the agent rule", text)
	}
}

// reviewSecret is a GitHub token shape the drafts secret scan flags.
const reviewSecret = "ghp_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8"

// dataFilesHolding answers every file under the app's data directory whose
// bytes hold s: the database, its write-ahead log and the JetStream blocks.
func dataFilesHolding(t *testing.T, app *App, s string) []string {
	t.Helper()
	var hits []string
	err := filepath.WalkDir(app.cfg.DataDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if b, err := os.ReadFile(p); err == nil && bytes.Contains(b, []byte(s)) {
			hits = append(hits, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

// TestApprovalRequestRecordMasksADescribedSubmit pins the record of an
// approval_request whose action describes straza__draft_submit: its
// arguments keep their shape, and each document and the note are masked
// as drafts.WithoutSecrets masks them, so the audit chain, the outbox, the
// JetStream blocks and the database's log hold no token. An
// approval_request that describes another tool keeps its arguments as
// sent.
func TestApprovalRequestRecordMasksADescribedSubmit(t *testing.T) {
	t.Parallel()
	r := newDoorRig(t, func(c *config.Config) { c.Approval.Preview.Enabled = true })
	ctx := context.Background()
	src := "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata: {name: asker}\nspec:\n  match: {users: [asker]}\n  rules:\n" +
		"    - id: gate\n      tools: [mcp.call]\n      apps: [straza]\n      toolNames: {allow: [draft_submit]}\n      effect: allow\n      mode: approve\n" +
		"      approve: {class: ticket, roles: [sec], ticketTTLSeconds: 86400, grantTTLSeconds: 3600}\n" +
		"    - id: ask\n      tools: [mcp.call]\n      apps: [straza]\n      toolNames: {allow: [approval_request]}\n      effect: allow\n"
	if _, err := r.app.store.Policies().Create(ctx, store.PolicySet{Name: "asker", Status: "active", YAMLSource: src}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.app.snapshots.Recompile(ctx); err != nil {
		t.Fatal(err)
	}
	asker, tok := r.agent(t, "asker")
	leaky := strings.Replace(draftApp("leak", "https://leak.example/mcp", "L."), "team-a", reviewSecret, 1)
	callArgs := map[string]any{"documents": []string{leaky}, "note": "use " + reviewSecret}
	if text, isErr, _ := callTool(t, r.base, tok, "straza__approval_request", map[string]any{
		"action": map[string]any{"tool": "mcp.call", "app": "straza", "tool_name": "draft_submit", "args": callArgs}}); isErr {
		t.Fatalf("approval_request = %q", text)
	}
	other := map[string]any{"action": map[string]any{"tool": "mcp.call", "app": "github", "tool_name": "get_me", "args": map[string]any{"q": "kept as sent"}}}
	callTool(t, r.base, tok, "straza__approval_request", other)

	recs := awaitMCPRecords(t, r.app, 2, func(d map[string]any) bool { return d["user"] == asker.ID && d["toolName"] == "approval_request" })
	var described struct {
		Action struct {
			App  string `json:"app"`
			Args struct {
				Documents []string `json:"documents"`
				Note      string   `json:"note"`
			} `json:"args"`
		} `json:"action"`
	}
	raw, _ := recs[0]["arguments"].(string)
	if err := json.Unmarshal([]byte(raw), &described); err != nil || described.Action.App != "straza" || len(described.Action.Args.Documents) != 1 {
		t.Fatalf("the described submit's recorded arguments = %q (%v), want its shape kept", raw, err)
	}
	if doc := described.Action.Args.Documents[0]; !strings.Contains(doc, redact.Mark) || !strings.Contains(doc, "leak.example") {
		t.Errorf("the recorded document = %q, want the token masked and the rest kept", doc)
	}
	if note := described.Action.Args.Note; strings.Contains(note, reviewSecret) || !strings.Contains(note, redact.Mark) {
		t.Errorf("the recorded note = %q, want the token masked", note)
	}
	if raw, _ := recs[1]["arguments"].(string); !strings.Contains(raw, "kept as sent") {
		t.Errorf("another tool's approval_request recorded %q, want its arguments as sent", raw)
	}
	for _, rec := range recs {
		if b, _ := json.Marshal(rec); strings.Contains(string(b), reviewSecret) {
			t.Errorf("the audit chain holds the token: %s", b)
		}
	}
	rows, err := r.app.store.Outbox().ListRecent(ctx, 5000)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if strings.Contains(row.CE, reviewSecret) {
			t.Errorf("an outbox row holds the token: %.300s", row.CE)
		}
	}
	if hits := dataFilesHolding(t, r.app, reviewSecret); len(hits) > 0 {
		t.Errorf("data files hold the token: %v", hits)
	}
}

// TestHookLaneDraftSubmitPreviewIsTheDigest pins the digest preview on the
// hook lane: an approval of straza draft_submit that /v1/decide opens
// stores the digest and the size of the forwarded arguments as its preview,
// never the documents.
func TestHookLaneDraftSubmitPreviewIsTheDigest(t *testing.T) {
	t.Parallel()
	r := newDoorRig(t, func(c *config.Config) { c.Approval.Preview.Enabled = true })
	ctx := context.Background()
	src := "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata: {name: hookheld}\nspec:\n  match: {users: [hooker]}\n  rules:\n" +
		"    - id: gate\n      tools: [mcp.call]\n      apps: [straza]\n      toolNames: {allow: [draft_submit]}\n      effect: allow\n      mode: approve\n      approve: {roles: [sec], timeoutSeconds: 1}\n"
	if _, err := r.app.store.Policies().Create(ctx, store.PolicySet{Name: "hookheld", Status: "active", YAMLSource: src}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.app.snapshots.Recompile(ctx); err != nil {
		t.Fatal(err)
	}
	_, tok := r.agent(t, "hooker")
	args := map[string]any{"documents": []string{strings.Replace(strings.Replace(draftApp("hk", "https://hk.example/mcp", "H."), "team-a", "Hunter2Hunter2", 1), "X-Team", "X-API-Key", 1)}}
	raw, _ := json.Marshal(args)
	sum := sha256.Sum256(raw)
	decide(t, r.base, tok, map[string]any{"kind": "tool.pre", "tool": "mcp.call", "app": "straza", "toolName": "draft_submit", "args": args})
	recs, err := r.app.approval.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, rec := range recs {
		if rec.Lane != "hook" {
			continue
		}
		seen = true
		if !strings.Contains(rec.ArgsPreview, hex.EncodeToString(sum[:])) || strings.Contains(rec.ArgsPreview, "Hunter2Hunter2") {
			t.Errorf("the hook lane's approval previews %q, want the digest %s and no document", rec.ArgsPreview, hex.EncodeToString(sum[:]))
		}
	}
	if !seen {
		t.Fatal("the decide opened no approval on the hook lane")
	}
}

// TestApprovalRequestSeesTheDraftingGrant pins that approval_request
// judges a described drafting call with the granted fact the gateway would
// give it: a holder with no rule on the tool is told that no approval is
// needed, a deny still denies, and a session without the role keeps the
// default deny.
func TestApprovalRequestSeesTheDraftingGrant(t *testing.T) {
	t.Parallel()
	r := newDoorRig(t)
	ctx := context.Background()
	src := "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata: {name: ask}\nspec:\n  match: {users: [norule, denied, bot]}\n  rules:\n" +
		"    - id: ask\n      tools: [mcp.call]\n      apps: [straza]\n      toolNames: {allow: [approval_request]}\n      effect: allow\n"
	deny := "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata: {name: no-denied}\nspec:\n  match: {users: [denied]}\n  rules:\n" +
		"    - id: no-submit\n      tools: [mcp.call]\n      apps: [straza]\n      toolNames: {deny: [draft_submit]}\n      effect: deny\n      reason: \"Straza: drafting is paused for this agent\"\n"
	for name, text := range map[string]string{"ask": src, "no-denied": deny} {
		if _, err := r.app.store.Policies().Create(ctx, store.PolicySet{Name: name, Status: "active", YAMLSource: text}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.app.snapshots.Recompile(ctx); err != nil {
		t.Fatal(err)
	}
	_, norule := r.agent(t, "norule")
	_, denied := r.agent(t, "denied")
	action := map[string]any{"action": map[string]any{"tool": "mcp.call", "app": "straza", "tool_name": "draft_submit",
		"args": map[string]any{"documents": []string{draftApp("nr", "https://nr.example/mcp", "NR.")}}}}
	for _, tc := range []struct {
		name, tok, want string
	}{
		{"a holder with no rule", norule, "Straza: no approval needed. The described call is already allowed by policy; just make the call"},
		{"a holder under a deny", denied, "Straza: denied by policy. The described call would be denied (Straza: drafting is paused for this agent); an approval ticket cannot override a deny"},
		{"a session without the role", sessionToken(t, r.base, "bot"), "Straza: denied by policy. The described call would be denied ("},
	} {
		if text, _, _ := callTool(t, r.base, tc.tok, "straza__approval_request", action); !strings.HasPrefix(text, tc.want) {
			t.Errorf("%s: approval_request = %q, want %q", tc.name, text, tc.want)
		}
	}
}

// TestMaskDescribedSubmit pins what the record of an approval_request
// keeps: a described submit is written again from what the handler reads,
// so neither a field the handler ignores, nor a key sent twice, nor a key
// in another case carries a document past the mask, and arguments that do
// not read as a submit's are masked whole. Every other call is kept byte
// for byte.
func TestMaskDescribedSubmit(t *testing.T) {
	t.Parallel()
	doc := strings.ReplaceAll(strings.Replace(draftApp("leak", "https://leak.example/mcp", "L."), "team-a", reviewSecret, 1), "\n", `\n`)
	doc = strings.ReplaceAll(doc, `"`, `\"`)
	cases := []struct {
		name, args string
		keep       bool
	}{
		{"a document", `{"action":{"tool":"mcp.call","app":"straza","tool_name":"draft_submit","args":{"documents":["` + doc + `"]}}}`, false},
		{"a key sent twice", `{"action":{"tool":"mcp.call","app":"straza","tool_name":"draft_submit","args":{"documents":["` + doc + `"],"documents":["x"]}}}`, false},
		{"a field the handler ignores", `{"action":{"tool":"mcp.call","app":"straza","tool_name":"draft_submit","args":{"documents":[],"extra":"` + reviewSecret + `"}},"more":"` + reviewSecret + `"}`, false},
		{"keys in another case", `{"Action":{"Tool":"mcp.call","App":"straza","Tool_Name":"draft_submit","Args":{"Documents":["` + doc + `"]}}}`, false},
		{"arguments that are not a submit's", `{"action":{"tool":"mcp.call","app":"straza","tool_name":"draft_submit","args":{"documents":"` + reviewSecret + `"}}}`, false},
		{"another tool", `{"action":{"tool":"mcp.call","app":"github","tool_name":"get_me","args":{"q":"` + reviewSecret + `"}}}`, true},
		{"arguments that do not read", `{"action":` + reviewSecret, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(maskDescribedSubmit(json.RawMessage(tc.args)))
			switch {
			case tc.keep && got != tc.args:
				t.Errorf("the arguments were changed to %s", got)
			case !tc.keep && strings.Contains(got, reviewSecret):
				t.Errorf("the recorded arguments hold the token: %s", got)
			case !tc.keep && !json.Valid([]byte(got)):
				t.Errorf("the recorded arguments are not JSON: %s", got)
			}
		})
	}
}

// waivedRemote is the remote server name at url whose registry record
// carries a plain word under an env name that marks it as a secret, with
// the given description.
func waivedRemote(name, url, description string) string {
	return strings.Replace(draftApp(name, url, description), "  remotes:\n",
		"  packages:\n    - registryType: npm\n      identifier: "+name+"-tools\n      environmentVariables:\n        - {name: WEATHER_API_KEY, value: name}\n  remotes:\n", 1)
}

// TestDraftSubmitLeavesTheWaiverToTheCheck pins the agent door on a name
// finding: a draft whose every intake refusal is about a name is stored
// with live state unread, as a clean one is, and the check decides. An
// agent that may not read the role gets no waiver, whatever live state
// holds: the finding stays refused and says that only a reader makes that
// change, and the draft.check record names the code. A new
// role keeps intake's own words.
func TestDraftSubmitLeavesTheWaiverToTheCheck(t *testing.T) {
	t.Parallel()
	armed, _, preRun := failingGeneration()
	r := &doorRig{newDraftsFixture(t, preRun)}
	ctx := context.Background()
	for _, name := range []string{"équipe", "dev\u200bops", " ops"} {
		if _, err := r.app.store.Roles().Create(ctx, store.Role{Name: name, Kind: store.RoleKindBusiness}); err != nil {
			t.Fatal(err)
		}
	}
	const readerRole = "Only someone who can read that role can change it under that name, because only a reader of the role may learn whether the stored name already holds that character. " +
		"Drafting the role équipe needs the scope identity:read, because a draft shows the live document of every role it names. " +
		"Ask an administrator for that grant, or leave the role équipe out of the draft."
	const readerName = "Only someone who can read that role can change it under that name, because only a reader of the role may learn whether the stored name already holds that character. Drafting the role "
	cases := []struct {
		name, document, code, fix string
	}{
		{"a new description of the role", draftRole("équipe", "New words."), "agent.ascii-name", readerRole},
		{"a new description of the role with an invisible character", draftRole("dev\u200bops", "New words."), "bundle.name-characters", readerName},
		{"a new description of the role whose name starts with a space", strings.Replace(draftRole("ops", "New words."), "name: ops", `name: " ops"`, 1), "bundle.name-characters", readerName},
		{"a new role's name outside ASCII", draftRole("équipe-2", "New."), "agent.ascii-name", "Rename it and submit again."},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, tok := r.agent(t, fmt.Sprintf("waiver%d", i))
			// Live state cannot be read while the submit runs, so a submit
			// that stores the draft read none.
			armed.Store(true)
			text, isErr, sc := submit(t, r.base, tok, map[string]any{"documents": []string{tc.document}})
			armed.Store(false)
			if isErr {
				t.Fatalf("submit = %q, want the draft stored for the check", text)
			}
			id, _ := sc["draft"].(string)
			if sc["checked"] != false || sc["next"] != fmt.Sprintf(wantSubmitNext, id) {
				t.Errorf("the submit answer = %v, want an unchecked draft that the check decides", sc)
			}
			// The submit woke the app's own checker while the live read still
			// failed, and a tick that met the failure leaves the draft to the
			// next tick 30 seconds on. So the test wakes that checker again
			// now that the read works, and waits for the check and its record.
			r.app.wakeDraftsChecker()
			r.awaitChecked(t, id)
			_, _, sc = status(t, r.base, tok, id)
			f := findingOf(sc["refused"], tc.code)
			if f == nil || findingOf(sc["warnings"], tc.code) != nil || sc["publishable"] != false || sc["next"] != fmt.Sprintf(wantFixRefused, id) {
				t.Fatalf("status = %v, want %s refused, not waived, and the agent told to fix it", sc, tc.code)
			}
			if fix := fmt.Sprint(f["fix"]); !strings.HasPrefix(fix, tc.fix) {
				t.Errorf("the refusal's fix reads %q, want it to start with %q", fix, tc.fix)
			}
			var recs []map[string]any
			waitFor(t, "the draft.check record of draft "+id, func() bool {
				recs = draftRecords(t, r.app, "draft.check", id)
				return len(recs) > 0
			})
			if len(recs) != 1 || !slices.Contains(agentCodes(recs[0]["refused"]), tc.code) {
				t.Errorf("draft.check records = %v, want one naming %s among the refused", recs, tc.code)
			}
		})
	}
}

// findingOf answers the finding of code in list, a keyed list of a tool
// answer, or nil.
func findingOf(list any, code string) map[string]any {
	items, _ := list.([]any)
	for _, f := range items {
		if m, _ := f.(map[string]any); m != nil && m["code"] == code {
			return m
		}
	}
	return nil
}

// TestDraftSubmitJudgesAValueBeforeItStores pins the agent door on a
// value finding, a plain word at a secret-named place or a mask sent back:
// the submit reads live state in the author's read view and refuses at
// once what the waiver does not lift, storing nothing and recording no
// draft.create and no draft.check, in the same words for the stored value
// and for a wrong guess, so no submit confirms a guess. A new
// server keeps intake's own words. A reader short of apps:write reads the
// value masked, so the stored value draws the same refusal as a wrong
// guess for that reader too. A submit that cannot read live state is
// refused with nothing stored.
func TestDraftSubmitJudgesAValueBeforeItStores(t *testing.T) {
	t.Parallel()
	armed, _, preRun := failingGeneration()
	r := &doorRig{newDraftsFixture(t, preRun)}
	weather := putServer(t, r.app, waivedRemote("weather", weatherURL, "The weather."))
	const readWeather = "Drafting the server weather needs the scope apps:read or the role mcp-admin-weather of the server weather, because a draft shows the live config of the servers it names and what each gives a role. " +
		"Ask an administrator for that grant, or leave the server weather out of the draft."
	const readerServer = "Only someone who can read that server can send that value, because only a reader of the server may learn whether its stored manifest already holds it. " + readWeather
	const readerMask = "Only someone who can read that server can send back a value Straza masked, because only a reader of the server may learn whether its stored manifest holds a value there. " + readWeather
	cases := []struct {
		name, document, object, fix string
	}{
		{"a new description of the server", waivedRemote("weather", weatherURL, "Changed."), "App/weather", readerServer},
		{"the value one byte off", strings.Replace(waivedRemote("weather", weatherURL, "Changed."), "value: name}", "value: names}", 1), "App/weather", readerServer},
		{"the answered manifest sent back", strings.Replace(maskedManifest(weather.Manifest), "The weather.", "Changed.", 1), "App/weather", readerMask},
		{"the same value for a new server", waivedRemote("forecast", "https://forecast.example/mcp", "Another."), "App/forecast", "Remove the value, or write a {placeholder} in its place."},
	}
	words := map[string]string{}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, tok := r.agent(t, fmt.Sprintf("value%d", i))
			text, isErr, _ := submit(t, r.base, tok, map[string]any{"documents": []string{tc.document}})
			if !isErr || !strings.HasPrefix(text, wantNotSaved+tc.object+": ") || !strings.Contains(text, " "+tc.fix) {
				t.Fatalf("submit = %q, want %s refused with the fix %q", text, tc.object, tc.fix)
			}
			words[tc.name] = strings.TrimPrefix(text, wantNotSaved)
			if stored := r.proposed(t, u); len(stored) != 0 {
				t.Errorf("the refusal stored %d drafts", len(stored))
			}
			for _, ev := range adminAuditEvents(t, r.app) {
				if ev["actorId"] == u.ID && strings.HasPrefix(fmt.Sprint(ev["action"]), "draft.") {
					t.Errorf("the refusal recorded %v", ev)
				}
			}
		})
	}
	if words["a new description of the server"] != words["the value one byte off"] {
		t.Errorf("the stored value and a wrong guess read differently, which tells the agent which one matches:\n%s\n%s", words["a new description of the server"], words["the value one byte off"])
	}
	// Two readers, since the door takes one submit every 10 seconds from
	// each.
	pat, pia := mkHuman(t, r.app, "pat", DraftConfigRole, "id-admins"), mkHuman(t, r.app, "pia", DraftConfigRole, "id-admins")
	stored, isErr, _ := submit(t, r.base, sessionToken(t, r.base, "pat"), map[string]any{"documents": []string{cases[0].document}})
	guess, _, _ := submit(t, r.base, sessionToken(t, r.base, "pia"), map[string]any{"documents": []string{cases[1].document}})
	if !isErr || stored != guess || !strings.Contains(stored, "Remove the value, or write a {placeholder} in its place.") {
		t.Errorf("a reader's submit of the stored value = %q\nand of a wrong guess = %q\nwant the same refusal of the value", stored, guess)
	}
	if n := append(r.proposed(t, pat), r.proposed(t, pia)...); len(n) != 0 {
		t.Errorf("the readers' refused submits stored %d drafts", len(n))
	}
	outage, tok := r.agent(t, "outage")
	armed.Store(true)
	text, isErr, _ := submit(t, r.base, tok, map[string]any{"documents": []string{waivedRemote("weather", weatherURL, "Changed.")}})
	armed.Store(false)
	if !isErr || text != "Straza: the draft was not saved, because Straza could not reach its database. Try again in a minute, and if it keeps failing ask an administrator to read the strazad log." {
		t.Errorf("a submit that cannot read live state = %q, want the store refusal", text)
	}
	if stored := r.proposed(t, outage); len(stored) != 0 {
		t.Errorf("the failed read stored %d drafts", len(stored))
	}
}

// agentCodes answers the code of every finding in list, a keyed list of a
// tool answer or the code list of a draft.check record.
func agentCodes(list any) []string {
	out := []string{}
	items, _ := list.([]any)
	for _, f := range items {
		if m, _ := f.(map[string]any); m != nil {
			out = append(out, fmt.Sprint(m["code"]))
		} else {
			out = append(out, fmt.Sprint(f))
		}
	}
	return out
}

// TestCheckerWaivesForAProposerWhoReadsTheObject pins that the checker
// applies the drafts routes' read rule to a draft of the straza-app door:
// a person who holds straza-draft-config and reads servers and roles gets
// the name waiver's warning, and for a value the scan's refusal, since
// every route masks that value from a reader short of apps:write, and a
// person who holds the drafting role alone gets the reader's refusal.
func TestCheckerWaivesForAProposerWhoReadsTheObject(t *testing.T) {
	t.Parallel()
	r := newDoorRig(t)
	ctx := context.Background()
	putServer(t, r.app, waivedRemote("weather", weatherURL, "The weather."))
	if _, err := r.app.store.Roles().Create(ctx, store.Role{Name: "équipe", Kind: store.RoleKindBusiness}); err != nil {
		t.Fatal(err)
	}
	reader := mkHuman(t, r.app, "pat", DraftConfigRole, "id-admins")
	blind := mkHuman(t, r.app, "sam", DraftConfigRole)
	actor := func(u store.User) store.DraftActor {
		return store.DraftActor{ID: u.ID, Name: u.Username, Via: laneSession, Client: "claude-code"}
	}
	has := func(fs []drafts.Finding, code, words string) bool {
		return slices.ContainsFunc(fs, func(f drafts.Finding) bool { return f.Code == code && strings.Contains(f.Sentence+" "+f.Fix, words) })
	}
	for _, tc := range []struct {
		name     string
		who      store.User
		document string
		code     string
		waived   bool
	}{
		{"a reader's change of the server", reader, waivedRemote("weather", weatherURL, "Changed."), "secret.value", false},
		{"a reader's change of the role", reader, draftRole("équipe", "New words."), "agent.ascii-name", true},
		{"a change of the server by a person who may not read it", blind, waivedRemote("weather", weatherURL, "Changed."), "secret.value", false},
	} {
		row := r.storeDraft(t, "straza-app", actor(tc.who), time.Now().Add(time.Hour), tc.document)
		if err := r.app.checkDrafts(ctx); err != nil {
			t.Fatal(err)
		}
		checked, _ := draftRow(t, r.app, row.ID)
		var av drafts.AgentVerdict
		if err := json.Unmarshal([]byte(checked.AgentVerdict), &av); err != nil || checked.CheckedRevision != 1 {
			t.Fatalf("%s: the checked draft = revision %d checked %d, verdict %q (%v)", tc.name, checked.Revision, checked.CheckedRevision, checked.AgentVerdict, err)
		}
		switch {
		case tc.waived && (len(av.Refused) != 0 || !has(av.Warnings, tc.code, "already holds")):
			t.Errorf("%s: refused %+v, warnings %+v; want no refusal and the waiver's warning", tc.name, av.Refused, av.Warnings)
		case !tc.waived && tc.who.ID == reader.ID && (!has(av.Refused, tc.code, "its name marks it as a secret") || has(av.Refused, tc.code, "Only someone who can read") || has(av.Warnings, tc.code, "already holds")):
			t.Errorf("%s: refused %+v, warnings %+v; want the scan's refusal and no warning", tc.name, av.Refused, av.Warnings)
		case !tc.waived && tc.who.ID != reader.ID && (!has(av.Refused, tc.code, "Only someone who can read that server can send that value") || has(av.Warnings, tc.code, "already holds")):
			t.Errorf("%s: refused %+v, warnings %+v; want the reader's refusal and no warning", tc.name, av.Refused, av.Warnings)
		}
	}
}
