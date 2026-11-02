package server

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/dpop"
	"github.com/strazahq/straza/internal/store"
)

// TestGrantIsNoSessionToken sends a git grant the route issued as the
// session credential to every route that takes a session token, under the
// DPoP scheme with a valid proof from the session's key and under Bearer,
// and wants each refused with 401. The session token itself passes the
// token check on the same routes, the positive control.
func TestGrantIsNoSessionToken(t *testing.T) {
	t.Parallel()
	r := newGrantRig(t, grantRigOpts{})
	s := r.session(t, "eve")
	status, out := r.grant(t, s, payAPI, "fetch", nil, nil)
	grant, _ := out["grant"].(string)
	if status != 200 || grant == "" {
		t.Fatalf("fetch grant = %d %v", status, out)
	}
	rig := &proofRig{app: r.app, base: r.base, key: r.key}
	sites := []proofSite{
		{name: "decide", method: http.MethodPost, path: "/v1/decide", body: `{"event":{"kind":"tool.pre","tool":"shell.exec","command":"ls"}}`},
		{name: "gateway", method: http.MethodPost, path: "/mcp", body: `{"jsonrpc":"2.0","id":1,"method":"ping"}`},
		{name: "gateway stream", method: http.MethodGet, path: "/mcp", stream: true},
		{name: "server endpoint", method: http.MethodPost, path: "/mcp/ghapp", body: `{"jsonrpc":"2.0","id":1,"method":"ping"}`},
		{name: "push", method: http.MethodGet, path: "/v1/push", stream: true},
		{name: "audit batch", method: http.MethodPost, path: "/v1/audit/batch", body: `{"events":[]}`},
		{name: "connect", method: http.MethodGet, path: "/v1/connect"},
		{name: "approvals", method: http.MethodGet, path: "/v1/self"},
		{name: "self revoke", method: http.MethodPost, path: "/v1/session/revoke"},
		{name: "admin", method: http.MethodGet, path: "/v1/admin/policies"},
		{name: "refresh", method: http.MethodPost, path: "/v1/checkin", bodyToken: true},
		{name: "grant resolve", method: http.MethodPost, path: "/v1/grants/resolve", body: `{"upstream":"` + payAPI + `"}`},
		{name: "grant", method: http.MethodPost, path: "/v1/grants", body: `{"upstream":"` + payAPI + `","op":"fetch"}`},
	}
	// The refresh lane and self revoke read the session row, which the
	// rig's sessions do not have, so their control is the grant routes'.
	// /v1/snapshot takes a session only on enterprise, so it gets its own
	// rig below.
	controlled := []string{"decide", "gateway", "gateway stream", "server endpoint", "push", "audit batch", "grant resolve", "grant"}
	for _, site := range sites {
		site.rig = rig
		t.Run(site.name, func(t *testing.T) {
			if slices.Contains(controlled, site.name) {
				ctl := r.session(t, "eve")
				ans := site.call(t, dpop.Scheme, ctl.token, rig.proof(t, r.key, site.method, site.path, rig.nonce(), ctl.token))
				if ans.status == http.StatusUnauthorized {
					t.Fatalf("the session token itself was refused: %d %q", ans.status, ans.err)
				}
			}
			for _, scheme := range []string{dpop.Scheme, schemeBearer} {
				var proofs []string
				if scheme == dpop.Scheme && !site.bodyToken {
					proofs = []string{rig.proof(t, r.key, site.method, site.path, rig.nonce(), grant)}
				}
				if ans := site.call(t, scheme, grant, proofs...); ans.status != http.StatusUnauthorized {
					t.Errorf("a grant under %s answered %d %q, want 401", scheme, ans.status, ans.err)
				}
				if site.bodyToken {
					break
				}
			}
		})
	}
}

// TestGrantIsNoSnapshotSession pins /v1/snapshot on enterprise, the one
// profile where it takes a session token: a grant minted by the same key
// set is refused under either scheme.
func TestGrantIsNoSnapshotSession(t *testing.T) {
	t.Parallel()
	ent, base := testApp(t, enterpriseProfile)
	key := newProofKey(t)
	grant, _, err := ent.tokens.MintGrant(authn.Grant{Audience: "https://ghapp.example.com", Subject: "user-1", Session: "ses-1",
		Kind: "human", JKT: dpop.Thumbprint(key.Public().(ed25519.PublicKey)), Server: "ghapp", ObjType: "repo",
		ObjValue: "Strazahq/pay-api", ObjID: "101", Op: "fetch", Decision: "dec-1"})
	if err != nil {
		t.Fatal(err)
	}
	rig := &proofRig{app: ent, base: base, key: key}
	site := proofSite{name: "snapshot", rig: rig, method: http.MethodGet, path: "/v1/snapshot"}
	if ans := site.call(t, dpop.Scheme, grant, rig.proof(t, key, site.method, site.path, rig.nonce(), grant)); ans.status != http.StatusUnauthorized {
		t.Errorf("a grant under DPoP answered %d %q, want 401", ans.status, ans.err)
	}
	if ans := site.call(t, schemeBearer, grant); ans.status != http.StatusUnauthorized || ans.err != snapshotBadSessionMsg {
		t.Errorf("a grant under Bearer answered %d %q, want 401 %q", ans.status, ans.err, snapshotBadSessionMsg)
	}
}

// TestGrantRefusedWhileAuditQueueStaysFull pins the grant under audit
// backpressure block with a full queue: the same fetch first gets its grant
// while the queue has room, the positive control. With the queue full it
// answers 503 with auditQueueFullMsg and no grant, and its record never
// reaches the outbox.
func TestGrantRefusedWhileAuditQueueStaysFull(t *testing.T) {
	t.Parallel()
	var sp *auditSpool
	r := newGrantRig(t, grantRigOpts{pre: func(a *App) {
		sp = newAuditSpool(true, a.log, func(c context.Context, e store.OutboxEvent) error {
			if strings.Contains(e.CE, `"id":"ce-held-`) {
				<-c.Done()
				return c.Err()
			}
			_, err := a.store.Outbox().Insert(c, e)
			return err
		})
		sp.ch = make(chan store.OutboxEvent, 1)
		sp.submitBound = 300 * time.Millisecond
		sp.drainBound = 50 * time.Millisecond
		a.audit = sp
	}})
	if status, out := r.grant(t, r.session(t, "eve"), payAPI, "fetch", nil, nil); status != 200 {
		t.Fatalf("with room in the queue the fetch answered %d %v", status, out)
	}
	_ = sp.submit(context.Background(), spoolRecord("ce-held-0", "x"))
	waitFor(t, "the loop to hold the first record", func() bool { return len(sp.ch) == 0 })
	_ = sp.submit(context.Background(), spoolRecord("ce-held-1", "x"))

	s := r.session(t, "eve")
	status, out := r.grant(t, s, payAPI, "fetch", nil, nil)
	if status != http.StatusServiceUnavailable || out["error"] != auditQueueFullMsg || out["grant"] != nil {
		t.Fatalf("answer = %d %v, want 503 with auditQueueFullMsg and no grant", status, out)
	}
	rows, err := r.app.store.Outbox().ListAfter(context.Background(), "", []string{"straza.audit.mcp"}, 100000)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if strings.Contains(row.CE, s.claims.Session) {
			t.Errorf("a record of the refused grant reached the outbox: %s", row.CE)
		}
	}
}

// TestGrantProofSites fires both grant routes with every way of presenting
// a session token that TestSessionProofVerifySites uses. The routes verify
// the token and its proof as every gateway request does, so each row
// answers and records as there, except that a token without cnf, which
// works on the other sites, gets sentence 1 with 403 and no record.
func TestGrantProofSites(t *testing.T) {
	t.Parallel()
	r := newGrantRig(t, grantRigOpts{})
	rig := &proofRig{app: r.app, base: r.base, key: r.key, other: newProofKey(t)}
	const tokenRejected = "session token rejected"
	sites := []proofSite{
		{name: "grant resolve", rig: rig, method: http.MethodPost, path: "/v1/grants/resolve",
			body: `{"upstream":"https://gitlab.com/x/y"}`, ok: http.StatusNotFound, refusal: tokenRejected},
		{name: "grant", rig: rig, method: http.MethodPost, path: "/v1/grants",
			body: `{"upstream":"https://gitlab.com/x/y","op":"fetch"}`, ok: http.StatusNotFound, refusal: tokenRejected},
	}
	var expects []proofExpect
	for i, s := range sites {
		for j, row := range proofRows() {
			s := s
			s.host = row.host
			t.Run(s.name+"/"+row.name, func(t *testing.T) {
				tag := fmt.Sprintf("grant-proof-%d-%d/1", i, j)
				c := r.session(t, "eve").claims
				c.Harness = tag
				tok := bindToken(t, r.app, c, r.key)
				if !row.bound {
					var err error
					if tok, _, err = r.app.tokens.Mint(c); err != nil {
						t.Fatal(err)
					}
				}
				var proofs []string
				if row.proofs != nil {
					proofs = row.proofs(t, s, tok)
				}
				if row.replay {
					if first := s.call(t, row.scheme, tok, proofs...); first.status != s.ok {
						t.Fatalf("the first use of the proof = %d %q, want %d", first.status, first.err, s.ok)
					}
				}
				ans := s.call(t, row.scheme, tok, proofs...)
				if !row.bound && row.scheme == schemeBearer {
					if ans.status != http.StatusForbidden || ans.err != grantUnboundMsg {
						t.Errorf("answer = %d %q, want 403 with sentence 1", ans.status, ans.err)
					}
				} else {
					checkProofAnswer(t, s, row, ans, len(proofs) > 0)
				}
				if row.want == "nonce" {
					retry := []string{s.rig.proof(t, s.rig.key, s.method, s.path, ans.header.Get(dpop.HeaderNonce), tok)}
					if again := s.call(t, row.scheme, tok, retry...); again.status != s.ok {
						t.Errorf("a proof signed with the answered nonce = %d %q, want %d", again.status, again.err, s.ok)
					}
				}
				expects = append(expects, proofExpect{rig: s.rig, tag: tag, claims: c, reason: row.reason})
			})
		}
	}
	checkProofRecords(t, sites, expects)
}

// TestGrantNoDB extends the no-DB rule to the git grant routes: a resolve,
// an issued fetch grant, a fetch the object gate refuses and a push grant
// without refs under a hold rule read only memory. The records ride the
// audit queue to the outbox, which is not a request-path read.
func TestGrantNoDB(t *testing.T) {
	t.Parallel()
	r := newGrantRig(t, grantRigOpts{})
	eve, pam, hol := r.session(t, "eve"), r.session(t, "pam"), r.session(t, "hol")
	before := r.cs.snapshot()
	if status, out := r.post(t, eve, "/v1/grants/resolve", `{"upstream":"`+payAPI+`"}`); status != 200 {
		t.Fatalf("resolve = %d %v", status, out)
	}
	if status, out := r.grant(t, eve, payAPI, "fetch", nil, nil); status != 200 {
		t.Fatalf("fetch = %d %v", status, out)
	}
	if status, out := r.grant(t, pam, "https://github.com/Strazahq/site", "fetch", nil, nil); status != 403 {
		t.Fatalf("uncovered fetch = %d %v", status, out)
	}
	if status, out := r.grant(t, hol, payAPI, "push", nil, nil); status != 200 {
		t.Fatalf("advertisement push = %d %v", status, out)
	}
	assertNoRepoAccess(t, r.cs, "git grants", before)
}
