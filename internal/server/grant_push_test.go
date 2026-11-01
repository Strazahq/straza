package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/authn"
)

// pendingOf waits for the one pending approval of s's session and answers
// its id.
func (r *grantRig) pendingOf(t *testing.T, s *grantSessionT) string {
	t.Helper()
	var id string
	waitFor(t, "a pending approval of the session", func() bool {
		recs, err := r.app.store.Approvals().List(context.Background(), "pending")
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range recs {
			if a.SessionID == s.claims.Session {
				id = a.ID
				return true
			}
		}
		return false
	})
	return id
}

// decide decides the approval id with verdict as the rig's approver.
func (r *grantRig) decide(t *testing.T, id, verdict string) {
	t.Helper()
	if _, err := r.app.approval.Decide(context.Background(), id, verdict, r.approver.ID, "console", "", ""); err != nil {
		t.Fatal(err)
	}
}

// approvalsOf answers how many approval requests s's session opened.
func (r *grantRig) approvalsOf(t *testing.T, s *grantSessionT) int {
	t.Helper()
	n := 0
	for _, state := range []string{"pending", "approved", "denied", "expired", "consumed"} {
		recs, err := r.app.store.Approvals().List(context.Background(), state)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range recs {
			if a.SessionID == s.claims.Session {
				n++
			}
		}
	}
	return n
}

// grantAnswer is one answer of POST /v1/grants.
type grantAnswer struct {
	status int
	out    map[string]any
}

// TestGrantPushApproval pins the approval of a gated push: the push without
// refs, which opens the advertisement, never asks a person, and the push
// with refs asks once, with the gateway's summary and lane, and its grant
// names the refs and the approval. git_push's schema refuses refs, so the
// grant also shows the schema check is skipped.
func TestGrantPushApproval(t *testing.T) {
	t.Parallel()
	r := newGrantRig(t, grantRigOpts{})

	t.Run("a push without refs asks nobody", func(t *testing.T) {
		s := r.session(t, "hol")
		status, out := r.grant(t, s, payAPI, "push", nil, nil)
		if status != 200 {
			t.Fatalf("advertisement grant = %d %v", status, out)
		}
		_, claims := grantClaims(t, out["grant"].(string))
		if _, ok := claims["refs"]; ok || claims["op"] != "push" {
			t.Errorf("claims = %v, want op push without refs", claims)
		}
		if _, ok := claims["apr"]; ok {
			t.Errorf("an advertisement grant names an approval: %v", claims["apr"])
		}
		if n := r.approvalsOf(t, s); n != 0 {
			t.Errorf("the advertisement grant opened %d approval requests, want none", n)
		}
		recs := r.records(t, s)
		if len(recs) != 1 || recs[0].data["effect"] != "allow" || recs[0].data["ruleId"] != "hold-git-push" ||
			recs[0].data["arguments"] != `{"repo":"Strazahq/pay-api"}` {
			t.Errorf("records = %v, want one allow under hold-git-push", recs)
		}
	})

	t.Run("a push with refs asks once", func(t *testing.T) {
		s := r.session(t, "hol")
		ch := asyncGrant(t, r, s, payAPI, "push", mainRef)
		id := r.pendingOf(t, s)
		rec, err := r.app.store.Approvals().GetByID(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Summary != "mcp.call ghapp:git_push" || rec.Lane != "gateway" {
			t.Errorf("approval summary %q lane %q, want the gateway's", rec.Summary, rec.Lane)
		}
		r.decide(t, id, "approved")
		ans := <-ch
		if ans.status != 200 {
			t.Fatalf("push grant = %d %v", ans.status, ans.out)
		}
		_, claims := grantClaims(t, ans.out["grant"].(string))
		raw, _ := json.Marshal(claims["refs"])
		var got []authn.GrantRef
		if err := json.Unmarshal(raw, &got); err != nil || len(got) != 1 || got[0] != mainRef[0] || claims["apr"] != id {
			t.Errorf("refs %s apr %v, want %v and %s", raw, claims["apr"], mainRef, id)
		}
		refs, _ := json.Marshal(mainRef)
		recs := r.records(t, s)
		if len(recs) != 1 {
			t.Fatalf("%d records, want one: %v", len(recs), recs)
		}
		wantArgs := `{"repo":"Strazahq/pay-api","branch":"main","refs":` + string(refs) + `}`
		if g, _ := recs[0].data["grant"].(map[string]any); g["approval"] != id || recs[0].data["arguments"] != wantArgs {
			t.Errorf("record %v, want approval %s and arguments %s", recs[0].data, id, wantArgs)
		}
		if n := r.approvalsOf(t, s); n != 1 {
			t.Errorf("the push opened %d approval requests, want one", n)
		}
	})
}

// TestGrantApprovalRetry pins the held grant: a request that ends before
// the decision answers 202 with the approval and its expiry and writes no
// record, a retry attaches to the same request, the retry after the
// approval gets the grant once, and the next retry asks again.
func TestGrantApprovalRetry(t *testing.T) {
	t.Parallel()
	r := newGrantRig(t, grantRigOpts{})
	s := r.session(t, "hol")
	held := func() (string, time.Time) {
		t.Helper()
		status, out := r.grant(t, s, payAPI, "push", mainRef, intp(0))
		p, _ := out["pending"].(map[string]any)
		ref, _ := p["ref"].(string)
		exp, err := time.Parse(time.RFC3339, p["expiresAt"].(string))
		if status != 202 || ref == "" || err != nil {
			t.Fatalf("held grant = %d %v (%v), want 202 with the approval", status, out, err)
		}
		return ref, exp
	}
	first, exp := held()
	if until := time.Until(exp); until < 30*time.Second || until > 61*time.Second {
		t.Errorf("expiresAt is %v away, want the rule's 60 s window", until)
	}
	if again, _ := held(); again != first {
		t.Errorf("the retry opened approval %s, want it attached to %s", again, first)
	}
	if recs := r.records(t, s); len(recs) != 0 {
		t.Errorf("held answers wrote %d records, want none: %v", len(recs), recs)
	}
	r.decide(t, first, "approved")
	status, out := r.grant(t, s, payAPI, "push", mainRef, intp(0))
	if status != 200 {
		t.Fatalf("the retry after the approval = %d %v, want the grant", status, out)
	}
	if _, claims := grantClaims(t, out["grant"].(string)); claims["apr"] != first {
		t.Errorf("apr = %v, want %s", claims["apr"], first)
	}
	if next, _ := held(); next == first {
		t.Errorf("a second retry used approval %s again", first)
	}
	recs := r.records(t, s)
	if len(recs) != 1 || recs[0].data["effect"] != "allow" {
		t.Fatalf("records = %v, want the one issued grant", recs)
	}
	start := time.Now()
	if status, _ := r.grant(t, r.session(t, "hol"), payAPI, "push", mainRef, intp(1)); status != 202 || time.Since(start) > 20*time.Second {
		t.Errorf("waitSeconds 1 answered %d after %v, want 202 within the cap", status, time.Since(start))
	}
}

// TestGrantApprovalEnds pins an approval that a person denies and one that
// expires: each answers 403 with the sentence and writes one deny record
// under the allowing rule.
func TestGrantApprovalEnds(t *testing.T) {
	t.Parallel()
	r := newGrantRig(t, grantRigOpts{})
	cases := []struct {
		name, user, rule, words string
		deny                    bool
	}{
		{"denied", "hol", "hold-git-push", "approval denied by secops", true},
		{"expired", "qui", "hold-git-push-quick", "approval request expired after 1s with no decision", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := r.session(t, tc.user)
			ch := asyncGrant(t, r, s, payAPI, "push", mainRef)
			if tc.deny {
				r.decide(t, r.pendingOf(t, s), "denied")
			}
			ans := <-ch
			msg, _ := ans.out["error"].(string)
			if ans.status != 403 || !strings.Contains(msg, tc.words) || ans.out["grant"] != nil {
				t.Fatalf("answer = %d %v, want 403 with %q", ans.status, ans.out, tc.words)
			}
			recs := r.records(t, s)
			if len(recs) != 1 {
				t.Fatalf("%d records, want one: %v", len(recs), recs)
			}
			d := recs[0].data
			if d["event"] != "grant" || d["effect"] != "deny" || d["ruleId"] != tc.rule || d["reason"] != msg || d["grant"] != nil {
				t.Errorf("record = %v, want a deny under %s with the answer's sentence", d, tc.rule)
			}
		})
	}
}

// TestGrantTicket pins a ticket rule on git_push. A push answers 403 at
// once with the ticket's sentence in git's words, never 202 and never the
// await tool, and writes one deny record as /mcp does. The same push after
// the approver allows the ticket gets its grant, which names the ticket.
// After a denial, the same push stays refused with the denial's sentence
// and opens no new ticket.
func TestGrantTicket(t *testing.T) {
	t.Parallel()
	r := newGrantRig(t, grantRigOpts{})
	ask := func(s *grantSessionT, refs []authn.GrantRef) (int, string) {
		t.Helper()
		start := time.Now()
		status, out := r.grant(t, s, payAPI, "push", refs, nil)
		if time.Since(start) > 10*time.Second {
			t.Errorf("the ticket answer took %v, want at once", time.Since(start))
		}
		if status == 200 {
			_, claims := grantClaims(t, out["grant"].(string))
			apr, _ := claims["apr"].(string)
			return status, apr
		}
		msg, _ := out["error"].(string)
		return status, msg
	}

	t.Run("allowed", func(t *testing.T) {
		t.Parallel()
		s := r.session(t, "tic")
		status, msg := ask(s, mainRef)
		if status != 403 || !strings.HasPrefix(msg, "Straza: approval ticket ") ||
			!strings.Contains(msg, "Run the same git command again once it is approved.") || strings.Contains(msg, "straza__approval_await") {
			t.Fatalf("first push = %d %q, want 403 with the ticket's git sentence", status, msg)
		}
		recs := r.records(t, s)
		if len(recs) != 1 || recs[0].data["effect"] != "deny" || recs[0].data["ruleId"] != "ticket-git-push" || recs[0].data["reason"] != msg {
			t.Fatalf("records = %v, want one deny under ticket-git-push with the answer's sentence", recs)
		}
		ticket := r.pendingOf(t, s)
		r.decide(t, ticket, "approved")
		if status, apr := ask(s, mainRef); status != 200 || apr != ticket {
			t.Fatalf("the push after the approval = %d %q, want the grant naming ticket %s", status, apr, ticket)
		}
		if recs := r.records(t, s); len(recs) != 2 || recs[1].data["effect"] != "allow" {
			t.Errorf("records = %v, want the refusal and then the issued grant", recs)
		}
	})

	t.Run("denied", func(t *testing.T) {
		t.Parallel()
		s := r.session(t, "tic")
		other := []authn.GrantRef{sha1Ref("refs/heads/dev")}
		if status, _ := ask(s, other); status != 403 {
			t.Fatalf("first push = %d, want 403", status)
		}
		r.decide(t, r.pendingOf(t, s), "denied")
		status, msg := ask(s, other)
		if status != 403 || !strings.Contains(msg, "was denied by secops, so git did not run") {
			t.Errorf("the push after the denial = %d %q, want 403 with the denial's sentence", status, msg)
		}
		if n := r.approvalsOf(t, s); n != 1 {
			t.Errorf("the session opened %d tickets, want one", n)
		}
	})
}

// TestGrantRefsInFingerprint pins that the refs are part of the approval's
// fingerprint: once a person approves a push of refs A, a push of refs B
// opens a new approval, and the push of refs A still gets its grant with
// the first approval.
func TestGrantRefsInFingerprint(t *testing.T) {
	t.Parallel()
	r := newGrantRig(t, grantRigOpts{})
	s := r.session(t, "hol")
	held := func(refs []authn.GrantRef) string {
		t.Helper()
		status, out := r.grant(t, s, payAPI, "push", refs, intp(0))
		p, _ := out["pending"].(map[string]any)
		ref, _ := p["ref"].(string)
		if status != 202 || ref == "" {
			t.Fatalf("push = %d %v, want 202 with an approval", status, out)
		}
		return ref
	}
	first := held(mainRef)
	r.decide(t, first, "approved")
	other := []authn.GrantRef{{Ref: "refs/heads/main", Old: mainRef[0].Old, New: "0123456789abcdef0123456789abcdef01234567"}}
	if second := held(other); second == first {
		t.Fatalf("the push of other refs used approval %s, which allowed refs %v", first, mainRef)
	}
	status, out := r.grant(t, s, payAPI, "push", mainRef, intp(0))
	if status != 200 {
		t.Fatalf("the approved refs = %d %v, want the grant", status, out)
	}
	if _, claims := grantClaims(t, out["grant"].(string)); claims["apr"] != first {
		t.Errorf("apr = %v, want %s", claims["apr"], first)
	}
}
