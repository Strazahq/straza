package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// selfRequestsRig seeds pending records straight into the store, each one
// second newer than the last, and reads both lanes of the waiting
// requests.
type selfRequestsRig struct {
	t    *testing.T
	app  *App
	base string
	at   time.Time
}

func newSelfRequestsRig(t *testing.T) *selfRequestsRig {
	t.Helper()
	app, base := testApp(t)
	return &selfRequestsRig{t: t, app: app, base: base, at: time.Now().UTC().Add(-time.Hour).Truncate(time.Second)}
}

// role creates a role named name and, when implies is set, makes it imply
// that role.
func (g *selfRequestsRig) role(name, implies string) {
	g.t.Helper()
	ctx := context.Background()
	r, err := g.app.store.Roles().Create(ctx, store.Role{Name: name})
	if err != nil {
		g.t.Fatal(err)
	}
	if implies != "" {
		target, err := g.app.store.Roles().GetByName(ctx, implies)
		if err != nil {
			g.t.Fatal(err)
		}
		if err := g.app.store.Roles().AddImplication(ctx, r.ID, target.ID); err != nil {
			g.t.Fatal(err)
		}
	}
	g.app.resolver.Bump()
}

// record inserts a pending record of the requester, shaped by shape, and
// answers its id.
func (g *selfRequestsRig) record(requester store.User, shape func(*store.Approval)) string {
	g.t.Helper()
	g.at = g.at.Add(time.Second)
	a := store.Approval{
		SessionID: "s-" + requester.Username, UserID: requester.ID, Username: requester.Username,
		RuleID: "r-self", SetName: "g", ArgvHash: "sha256:" + g.at.Format(time.RFC3339), Lane: "gateway",
		Summary: "mcp.call demo-tools:get-sum", Justification: "the agent's words",
		State: "pending", CreatedAt: g.at, ExpiresAt: time.Now().Add(time.Hour),
	}
	if shape != nil {
		shape(&a)
	}
	a, err := g.app.store.Approvals().Insert(context.Background(), a)
	if err != nil {
		g.t.Fatal(err)
	}
	return a.ID
}

// self reads GET /v1/self/requests with bearer and answers the raw body
// and the rows.
func (g *selfRequestsRig) self(bearer, scope string) ([]byte, []map[string]any) {
	g.t.Helper()
	code, raw, _ := adminBytes(g.t, http.MethodGet, g.base+"/v1/self/requests?scope="+scope, bearer, "", nil)
	if code != http.StatusOK {
		g.t.Fatalf("self %s = %d %s", scope, code, raw)
	}
	var out struct {
		Requests []map[string]any `json:"requests"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		g.t.Fatalf("self %s: %v (%s)", scope, err, raw)
	}
	return raw, out.Requests
}

// device reads GET /v1/approver/pending with an approver device token.
func (g *selfRequestsRig) device(token, scope string) []map[string]any {
	g.t.Helper()
	var rows []map[string]any
	if code := adminReq(g.t, http.MethodGet, g.base+"/v1/approver/pending?scope="+scope, token, nil, &rows); code != http.StatusOK {
		g.t.Fatalf("device %s = %d", scope, code)
	}
	return rows
}

// rowIDs answers the ids of rows in order.
func rowIDs(rows []map[string]any) []string {
	out := []string{}
	for _, r := range rows {
		id, _ := r["id"].(string)
		out = append(out, id)
	}
	return out
}

// assertNoChallenge fails when a raw answer of the sign-in read carries
// anything a signature could be made over.
func assertNoChallenge(t *testing.T, who string, raw []byte) {
	t.Helper()
	for _, leak := range []string{`"challenge"`, "nonce", `"signature"`, `"ts"`} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("%s: the sign-in read carries %s: %s", who, leak, raw)
		}
	}
}

// TestSelfRequestsDecidableMatchesDeviceLane pins scope=decidable against
// the device lane: for a sponsor of an agent with a held call, a holder of
// an approver role, a holder through a composed role, the requester on a
// confirm rule, a straza-admin for a request with no decider named and a
// person who may decide nothing, the sign-in read answers the rows the
// device lane answers that person's device, row id by row id, with the same
// fields but challenge, and carries nothing a signature could be made over.
func TestSelfRequestsDecidableMatchesDeviceLane(t *testing.T) {
	t.Parallel()
	g := newSelfRequestsRig(t)
	ctx := context.Background()
	g.role("sec-approvers", "")
	g.role("chg-approvers", "")
	g.role("release-leads", "chg-approvers")

	sam := mkHuman(t, g.app, "sam")
	rita := mkHuman(t, g.app, "rita", "sec-approvers")
	cory := mkHuman(t, g.app, "cory", "release-leads")
	quinn := mkHuman(t, g.app, "quinn")
	ada := mkHuman(t, g.app, "ada", AdminRole)
	nora := mkHuman(t, g.app, "nora")
	bot, err := g.app.store.Users().Create(ctx, store.User{Username: "sam-bot", UserType: store.UserTypeAgent, Sponsor: "sam"})
	if err != nil {
		t.Fatal(err)
	}
	other := mkHuman(t, g.app, "olga")

	held := g.record(bot, func(a *store.Approval) { a.ApproverUsers = []string{"sam"} })
	byRole := g.record(other, func(a *store.Approval) { a.ApproverRoles = []string{"sec-approvers"} })
	byComposed := g.record(other, func(a *store.Approval) { a.ApproverRoles = []string{"chg-approvers"} })
	confirm := g.record(quinn, func(a *store.Approval) { a.Mode = "confirm" })
	unnamed := g.record(other, nil)
	g.record(other, func(a *store.Approval) {
		a.ApproverRoles = []string{"sec-approvers"}
		a.State = "approved"
	})

	for _, tc := range []struct {
		who  string
		user store.User
		want []string
	}{
		{"the sponsor of an agent with a held call", sam, []string{held}},
		{"a holder of an approver role", rita, []string{byRole}},
		{"a holder through a composed role", cory, []string{byComposed}},
		{"the requester on a confirm rule", quinn, []string{confirm}},
		{"a straza-admin for a request with no decider named", ada, []string{unnamed}},
		{"a person who may decide nothing", nora, []string{}},
	} {
		_, _, token := enrollApprover(t, g.app, tc.user.ID)
		_, session := storedSession(t, g.app, tc.user.ID, "", "console")
		deviceRows := g.device(token, "decidable")
		raw, selfRows := g.self(session, "decidable")
		got, lane := rowIDs(selfRows), rowIDs(deviceRows)
		if string(mustJSON(got)) != string(mustJSON(lane)) || string(mustJSON(got)) != string(mustJSON(tc.want)) {
			t.Errorf("%s: the sign-in read = %v, the device lane = %v, want both %v", tc.who, got, lane, tc.want)
			continue
		}
		for i := range deviceRows {
			if deviceRows[i]["challenge"] == nil {
				t.Errorf("%s: the device lane's row %s has no challenge, so the comparison proves nothing", tc.who, lane[i])
			}
			delete(deviceRows[i], "challenge")
			if want, have := mustJSON(deviceRows[i]), mustJSON(selfRows[i]); string(have) != string(want) {
				t.Errorf("%s: row %s\n got %s\nwant %s", tc.who, got[i], have, want)
			}
		}
		assertNoChallenge(t, tc.who, raw)
	}
}

// TestSelfRequestsOnlyOwnOrDecidable pins that a person never reads a
// request that is neither theirs to decide nor raised by them: two people,
// each holding a role the other lacks, and four pending requests, one each
// may decide and one each raised. scope=mine answers the caller's own
// pending requests and no decided one, and carries no challenge either.
func TestSelfRequestsOnlyOwnOrDecidable(t *testing.T) {
	t.Parallel()
	g := newSelfRequestsRig(t)
	g.role("ana-role", "")
	g.role("ben-role", "")
	g.role("nobody-role", "")
	ana := mkHuman(t, g.app, "ana", "ana-role")
	ben := mkHuman(t, g.app, "ben", "ben-role")
	other := mkHuman(t, g.app, "olga")

	anaDecides := g.record(other, func(a *store.Approval) { a.ApproverRoles = []string{"ana-role"} })
	benDecides := g.record(other, func(a *store.Approval) { a.ApproverRoles = []string{"ben-role"} })
	anaRaised := g.record(ana, func(a *store.Approval) { a.ApproverRoles = []string{"nobody-role"} })
	benRaised := g.record(ben, func(a *store.Approval) { a.ApproverRoles = []string{"nobody-role"} })
	g.record(ana, func(a *store.Approval) {
		a.ApproverRoles = []string{"nobody-role"}
		a.State = "denied"
		decided := time.Now()
		a.DecidedAt = &decided
	})
	_, anaSession := storedSession(t, g.app, ana.ID, "", "self-service")
	_, benSession := storedSession(t, g.app, ben.ID, "", "self-service")

	for _, tc := range []struct {
		who, bearer, scope string
		want               []string
	}{
		{"ana", anaSession, "decidable", []string{anaDecides}},
		{"ana", anaSession, "mine", []string{anaRaised}},
		{"ben", benSession, "decidable", []string{benDecides}},
		{"ben", benSession, "mine", []string{benRaised}},
	} {
		raw, rows := g.self(tc.bearer, tc.scope)
		if got := rowIDs(rows); string(mustJSON(got)) != string(mustJSON(tc.want)) {
			t.Errorf("%s %s = %v, want %v", tc.who, tc.scope, got, tc.want)
		}
		for _, r := range rows {
			if tc.scope == "mine" && r["state"] != "pending" {
				t.Errorf("%s mine carries a row in state %v", tc.who, r["state"])
			}
		}
		assertNoChallenge(t, tc.who+" "+tc.scope, raw)
	}
}

// TestSelfRequestsMineKeepsAWaitingTicket pins that scope=mine lists a
// request that still waits however many settled records are newer: ben's
// waiting ticket sits behind 500 newer settled records of other people, and
// ana's behind 100 newer settled records of her own. Each reads their ticket.
func TestSelfRequestsMineKeepsAWaitingTicket(t *testing.T) {
	t.Parallel()
	g := newSelfRequestsRig(t)
	ana := mkHuman(t, g.app, "ana")
	ben := mkHuman(t, g.app, "ben")
	other := mkHuman(t, g.app, "olga")
	ticket := func(a *store.Approval) {
		a.Class = "ticket"
		a.ApproverRoles = []string{"nobody"}
	}
	settled := func(a *store.Approval) {
		decided := time.Now()
		a.State, a.DecidedAt, a.ApproverRoles = "denied", &decided, []string{"nobody"}
	}
	benTicket := g.record(ben, ticket)
	for range 500 {
		g.record(other, settled)
	}
	anaTicket := g.record(ana, ticket)
	for range 100 {
		g.record(ana, settled)
	}
	_, anaSession := storedSession(t, g.app, ana.ID, "", "self-service")
	_, benSession := storedSession(t, g.app, ben.ID, "", "console")

	for _, tc := range []struct {
		who, bearer, want string
	}{
		{"ana, behind 100 newer settled records of her own", anaSession, anaTicket},
		{"ben, behind 500 newer settled records of other people", benSession, benTicket},
	} {
		_, rows := g.self(tc.bearer, "mine")
		if got := rowIDs(rows); len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s: mine = %v, want the waiting ticket %s", tc.who, got, tc.want)
		}
	}
}

// TestSelfRequestsScope pins the scope parameter: it is required, and an
// absent or unknown value answers 400 with a sentence that names both.
func TestSelfRequestsScope(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	lin := mkHuman(t, app, "lin")
	_, session := storedSession(t, app, lin.ID, "", "console")
	for _, query := range []string{"", "?scope=", "?scope=all", "?scope=history"} {
		var out struct {
			Error string `json:"error"`
		}
		if code := adminReq(t, http.MethodGet, base+"/v1/self/requests"+query, session, nil, &out); code != http.StatusBadRequest || out.Error != selfRequestsScopeRefusal {
			t.Errorf("%q = %d %q, want 400 %q", query, code, out.Error, selfRequestsScopeRefusal)
		}
	}
}

// TestSelfRequestsWriteNothing pins that the sign-in read writes nothing:
// no challenge row, no change to the person's device's last-seen time and
// no record, on either scope. The device lane's read on the same rig then
// writes a challenge and moves the time, so the instruments are live.
func TestSelfRequestsWriteNothing(t *testing.T) {
	t.Parallel()
	g := newSelfRequestsRig(t)
	ctx := context.Background()
	g.role("sec-approvers", "")
	lin := mkHuman(t, g.app, "lin", "sec-approvers")
	other := mkHuman(t, g.app, "olga")
	g.record(other, func(a *store.Approval) { a.ApproverRoles = []string{"sec-approvers"} })
	g.record(lin, func(a *store.Approval) { a.ApproverRoles = []string{"nobody"} })
	_, deviceID, token := enrollApprover(t, g.app, lin.ID)
	_, session := storedSession(t, g.app, lin.ID, "", "self-service")
	idToken, err := g.app.tokens.MintIDToken(lin.ID, "straza", 10*time.Minute, lin.Username, lin.Email)
	if err != nil {
		t.Fatal(err)
	}
	far := time.Now().Add(24 * time.Hour)
	if _, err := g.app.store.Approvers().PurgeChallengesBefore(ctx, far); err != nil {
		t.Fatal(err)
	}
	before, err := g.app.store.Approvers().GetDevice(ctx, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	outbox := func() int {
		rows, err := g.app.store.Outbox().ListRecent(ctx, 1000)
		if err != nil {
			t.Fatal(err)
		}
		return len(rows)
	}
	records := outbox()

	for _, bearer := range []string{session, idToken} {
		for _, scope := range []string{"decidable", "mine"} {
			if _, rows := g.self(bearer, scope); len(rows) != 1 {
				t.Fatalf("%s read %d rows, want 1", scope, len(rows))
			}
		}
	}
	if n, err := g.app.store.Approvers().PurgeChallengesBefore(ctx, far); err != nil || n != 0 {
		t.Errorf("the sign-in read wrote %d challenge rows (%v), want none", n, err)
	}
	after, err := g.app.store.Approvers().GetDevice(ctx, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	if string(mustJSON(after.LastSeenAt)) != string(mustJSON(before.LastSeenAt)) {
		t.Errorf("the sign-in read moved last_seen from %v to %v", before.LastSeenAt, after.LastSeenAt)
	}
	if n := outbox(); n != records {
		t.Errorf("the sign-in read wrote %d records, want none", n-records)
	}

	g.device(token, "decidable")
	if n, err := g.app.store.Approvers().PurgeChallengesBefore(ctx, far); err != nil || n != 1 {
		t.Errorf("the device lane wrote %d challenge rows (%v), want 1: the instrument is dead", n, err)
	}
	if moved, _ := g.app.store.Approvers().GetDevice(ctx, deviceID); moved.LastSeenAt == nil {
		t.Error("the device lane left last_seen unset: the instrument is dead")
	}
}

// List fails while "approvals-list" is armed. Both scopes of
// GET /v1/self/requests read the pending records through it.
func (a faultApprovals) List(ctx context.Context, state string) ([]store.Approval, error) {
	if err := a.f.fault("approvals-list"); err != nil {
		return nil, err
	}
	return a.ApprovalRepo.List(ctx, state)
}

// TestSelfRequestsReadFailure pins the 500 of a read the store cannot
// answer: on both scopes, a sentence a person can act on that names no
// server process.
func TestSelfRequestsReadFailure(t *testing.T) {
	t.Parallel()
	app, base, faults := testAppFault(t)
	lin := mkHuman(t, app, "lin")
	_, session := storedSession(t, app, lin.ID, "", "self-service")
	faults.arm("approvals-list", errors.New("database is locked"))
	const want = "Straza could not read the waiting requests, so it cannot list them. Try again, and tell your administrator if it keeps failing."
	for _, scope := range []string{"decidable", "mine"} {
		var out struct {
			Error string `json:"error"`
		}
		if code := adminReq(t, http.MethodGet, base+"/v1/self/requests?scope="+scope, session, nil, &out); code != http.StatusInternalServerError || out.Error != want {
			t.Errorf("%s = %d %q, want 500 %q", scope, code, out.Error, want)
		}
	}
}
