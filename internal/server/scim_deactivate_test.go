package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/strazahq/straza/internal/scim"
	"github.com/strazahq/straza/internal/store"
)

// scimSend sends one SCIM request with the raw body and answers the status,
// the request id the server answered with and the decoded body.
func scimSend(t *testing.T, method, url, token, body string) (int, string, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/scim+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, resp.Header.Get(requestIDHeader), out
}

// TestSCIMDeactivationCommitsWithItsRevocation pins the SCIM deactivation
// an identity manager such as midPoint sends, a PATCH of active false, a PUT
// with active false and a DELETE, which write the disabled status, the
// revocation row of origin scim and its straza.revocation.user event
// together. A refused write answers a SCIM 500 whose detail says the
// deactivation was not saved, with one Error record and one error count,
// and leaves the user active with its session and grant, refused only by
// this server's denylist until strazactl users unlock. The identity
// manager's next reconciliation resends it, and the resend lands each record
// once. A write without a clear answer is settled by asking the store. A
// committed one finishes as a clean commit, one that did not commit gets the
// not-saved detail, and one the store cannot settle keeps the unsure detail.
func TestSCIMDeactivationCommitsWithItsRevocation(t *testing.T) {
	t.Parallel()
	log, buf := captureLogger()
	app, base, fs := testAppFaultLog(t, log)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	token := mintProvisioningToken(t, base, bearer)
	ctx := context.Background()
	const reason = "deactivated via SCIM"
	lostConn := &pgconn.PgError{Severity: "FATAL", Code: "08006", Message: "connection failure during the commit"}
	connClosed := closedConnErr{}

	writes := []struct {
		name, method, route, identity string
		body                          func(username string) string
		ok                            int
	}{
		{name: "PATCH active false", method: http.MethodPatch, route: "PATCH /scim/v2/Users/{id}",
			identity: "straza.identity.updated", ok: http.StatusOK,
			body: func(string) string {
				return `{"schemas":["` + scim.SchemaPatch + `"],"Operations":[{"op":"replace","path":"active","value":false}]}`
			}},
		{name: "PUT with active false", method: http.MethodPut, route: "PUT /scim/v2/Users/{id}",
			identity: "straza.identity.updated", ok: http.StatusOK,
			body: func(username string) string {
				return `{"schemas":["` + scim.SchemaUser + `"],"userName":"` + username + `","externalId":"idm-` + username + `","active":false}`
			}},
		{name: "DELETE", method: http.MethodDelete, route: "DELETE /scim/v2/Users/{id}",
			identity: "straza.identity.deactivated", ok: http.StatusNoContent,
			body: func(string) string { return "" }},
	}
	faults := []struct {
		name   string
		arm    []string
		err    error
		unsure bool
		// landed marks a write that committed before its answer was lost.
		landed bool
	}{
		{name: "a healthy store"},
		{name: "the status write is refused", arm: []string{"users.write"}, err: pgDown},
		{name: "the revocation row is refused", arm: []string{"revocations"}, err: pgDown},
		{name: "the event is refused", arm: []string{"outbox:straza.revocation.user"}, err: pgDown},
		{name: "a commit whose connection failed before its answer", arm: []string{"users.write.lost"}, err: lostConn, landed: true},
		{name: "a commit whose connection pgx closed before its answer", arm: []string{"users.write.lost"}, err: connClosed, landed: true},
		{name: "a write that ends without an answer and did not commit", arm: []string{"users.write"}, err: lostConn},
		{name: "a write that ends without an answer the store cannot settle", arm: []string{"users.write", "users.committed"}, err: lostConn, unsure: true},
	}
	// holds checks what the store and this server hold for the user id
	// after n deactivations landed, 0 or 1.
	holds := func(t *testing.T, identity, id, session string, n int) {
		t.Helper()
		row, err := app.store.Users().GetByID(ctx, id)
		want := store.UserActive
		if n == 1 {
			want = store.UserDisabled
		}
		if err != nil || row.Status != want {
			t.Errorf("the user reads %q (%v), want %s", row.Status, err, want)
		}
		rows, err := app.store.Revocations().ListByTarget(ctx, store.RevokeUser, id)
		if err != nil || len(rows) != n {
			t.Errorf("revocation rows = %d (%v), want %d", len(rows), err, n)
		}
		for _, rv := range rows {
			if rv.Reason != reason || rv.Origin != store.RevocationOriginSCIM {
				t.Errorf("revocation row = %+v, want reason %q and origin scim", rv, reason)
			}
		}
		if got := eventsNaming(t, app, "straza.revocation.user", "user", id); len(got) != n {
			t.Errorf("straza.revocation.user events = %v, want %d", got, n)
		}
		var killed []map[string]any
		for _, rec := range eventsNaming(t, app, "straza.audit.identity", "user", id) {
			if rec["action"] == "user.killed" {
				killed = append(killed, rec)
			}
		}
		if len(killed) != n {
			t.Errorf("user.killed records = %v, want %d", killed, n)
		}
		for _, rec := range killed {
			if rec["origin"] != "scim" || rec["sessionsRevoked"] != float64(1) {
				t.Errorf("user.killed record = %v, want origin scim and one session", rec)
			}
		}
		updates := 0
		for _, rec := range eventsNaming(t, app, "straza.audit.admin", "user", id) {
			changed, _ := rec["changed"].([]any)
			if rec["action"] == actionUserUpdate && slices.Contains(changed, any("status")) {
				updates++
			}
		}
		if updates != n {
			t.Errorf("user.update records that name the status = %d, want %d", updates, n)
		}
		if got := eventsNaming(t, app, identity, "id", id); len(got) != n {
			t.Errorf("%s events = %v, want %d", identity, got, n)
		}
		if got := grantRemoveEvents(t, app, id); len(got) != n {
			t.Errorf("apps.grant.remove records = %v, want %d", got, n)
		}
		if got := userGrants(t, app, id); len(got) != 1-n {
			t.Errorf("grant rows = %d, want %d", len(got), 1-n)
		}
		wantSession := store.SessionActive
		if n == 1 {
			wantSession = store.SessionRevoked
		}
		if ses, err := app.store.Sessions().GetByID(ctx, session); err != nil || ses.Status != wantSession {
			t.Errorf("the user's session is %q (%v), want %s", ses.Status, err, wantSession)
		}
		if !app.denylist.userBlocked(id) {
			t.Error("this server's denylist admits the user")
		}
	}
	for i, w := range writes {
		for j, fault := range faults {
			t.Run(w.name+" with "+fault.name, func(t *testing.T) {
				username := fmt.Sprintf("leaver%d%d", i, j)
				id := scimCreateUser(t, base, token, `{"schemas":["`+scim.SchemaUser+`"],"userName":"`+username+`","externalId":"idm-`+username+`","active":true}`)
				session, _ := storedSession(t, app, id, "", "console")
				grantUserApp(t, app, "gh-"+username, id)
				counted := func() float64 {
					n, _, _ := metricSample(t, app, "straza_http_errors_total", map[string]string{"route": w.route, "status": "500"})
					return n
				}
				before := counted()
				send := func() (int, string, map[string]any) {
					return scimSend(t, w.method, base+"/scim/v2/Users/"+id, token, w.body(username))
				}
				for _, key := range fault.arm {
					fs.arm(key, fault.err)
				}
				code, reqID, out := send()
				fs.disarm()
				var tagged []string
				for _, rec := range errorRecords(buf) {
					if strings.Contains(rec, "correlation_id="+reqID) {
						tagged = append(tagged, rec)
					}
				}
				if fault.arm == nil || fault.landed {
					if code != w.ok {
						t.Fatalf("%s = %d %v, want %d", w.name, code, out, w.ok)
					}
					if w.ok == http.StatusOK && (out["id"] != id || out["active"] != false) {
						t.Errorf("%s answered %v, want the user resource with active false", w.name, out)
					}
					if len(tagged) != 0 || counted() != before {
						t.Errorf("Error records %q and %v error counts, want none", tagged, counted()-before)
					}
					holds(t, w.identity, id, session, 1)
					return
				}
				want := fmt.Sprintf(scimDeactivationNotSavedMsg, reqID)
				if fault.unsure {
					want = fmt.Sprintf(scimDeactivationUnsureMsg, reqID)
				}
				schemas, _ := out["schemas"].([]any)
				if code != http.StatusInternalServerError || out["status"] != "500" || out["detail"] != want ||
					len(schemas) != 1 || schemas[0] != scim.SchemaError {
					t.Errorf("%s = %d %v, want a SCIM 500 with the detail %q", w.name, code, out, want)
				}
				if len(tagged) != 1 || !strings.Contains(tagged[0], fault.err.Error()) || !strings.Contains(tagged[0], "status=500") {
					t.Errorf("Error records under %s = %q, want one with status 500 that names the cause", reqID, tagged)
				}
				if got := counted() - before; got != 1 {
					t.Errorf("error counts of %s = %v, want 1", w.route, got)
				}
				holds(t, w.identity, id, session, 0)
				// The detail says strazactl users unlock lifts this server's
				// refusal, which no revocation row backs.
				if code, _, out := callJSON(t, http.MethodPost, base+"/v1/admin/users/"+id+"/unlock", bearer, nil); code != http.StatusOK {
					t.Fatalf("unlock = %d %v, want 200", code, out)
				}
				if app.denylist.userBlocked(id) {
					t.Error("this server's denylist still refuses the user after the unlock")
				}
				// The identity manager's next reconciliation sees an active
				// user and sends the deactivation again.
				if code, _, out := send(); code != w.ok {
					t.Fatalf("the resend = %d %v, want %d", code, out, w.ok)
				}
				holds(t, w.identity, id, session, 1)
			})
		}
	}
}

// TestSCIMDeactivationPastTheBound pins the one record of a SCIM
// deactivation whose write answers only after adminWriteBound: the answer
// is the bound's SCIM 503, and the request leaves exactly one Error record
// and one error count, those of the 503. The write committed before its
// answer was lost, so the store shows that it committed and the handler
// finishes as after a clean commit: its held-back answer is a 200, the user
// reads disabled, its session is revoked with one user.killed record, its
// grant is removed with one apps.grant.remove record, and this server's
// denylist refuses it. It shrinks the bound, so it does not run in parallel.
func TestSCIMDeactivationPastTheBound(t *testing.T) {
	old := adminWriteBound
	adminWriteBound = 300 * time.Millisecond
	t.Cleanup(func() { adminWriteBound = old })
	logger, logs := captureLogger()
	app, base, ls := testAppLeaving(t, logger)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	token := mintProvisioningToken(t, base, loginDeviceFlow(t, base, "kim", "hunter2!"))
	id := scimCreateUser(t, base, token, `{"schemas":["`+scim.SchemaUser+`"],"userName":"late","externalId":"idm-late","active":true}`)
	session, _ := storedSession(t, app, id, "", "console")
	grantUserApp(t, app, "gh-late", id)
	const route = "PATCH /scim/v2/Users/{id}"
	ls.arm(afterUpdate, untilBound)
	logs.Reset()
	code, reqID, out := scimSend(t, http.MethodPatch, base+"/scim/v2/Users/"+id, token,
		`{"schemas":["`+scim.SchemaPatch+`"],"Operations":[{"op":"replace","path":"active","value":false}]}`)
	if detail, _ := out["detail"].(string); code != http.StatusServiceUnavailable || !strings.Contains(detail, "did not finish within") {
		t.Fatalf("PATCH past the bound = %d %v, want the bound's 503", code, out)
	}
	var records []string
	for _, rec := range errorRecords(logs) {
		if strings.Contains(rec, "correlation_id="+reqID) {
			records = append(records, rec)
		}
	}
	if len(records) != 1 || !strings.Contains(records[0], "status=503") || !strings.Contains(records[0], "answered 200") {
		t.Errorf("Error records under %s = %q, want the one record of the 503 that names the held-back 200", reqID, records)
	}
	for status, want := range map[string]float64{"503": 1, "500": 0} {
		if n, _, _ := metricSample(t, app, "straza_http_errors_total", map[string]string{"route": route, "status": status}); n != want {
			t.Errorf("straza_http_errors_total %s %s = %v, want %v", route, status, n, want)
		}
	}
	if row, err := app.store.Users().GetByID(context.Background(), id); err != nil || row.Status != store.UserDisabled {
		t.Errorf("the user reads %q (%v), want disabled, because the write committed", row.Status, err)
	}
	if ses, err := app.store.Sessions().GetByID(context.Background(), session); err != nil || ses.Status != store.SessionRevoked {
		t.Errorf("the user's session is %q (%v), want revoked", ses.Status, err)
	}
	if got := eventsNaming(t, app, "straza.audit.identity", "user", id); len(got) != 1 || got[0]["action"] != "user.killed" {
		t.Errorf("straza.audit.identity records of the user = %v, want one user.killed", got)
	}
	if got := userGrants(t, app, id); len(got) != 0 {
		t.Errorf("grant rows = %d, want none, because the deactivation committed", len(got))
	}
	if got := grantRemoveEvents(t, app, id); len(got) != 1 {
		t.Errorf("apps.grant.remove records = %v, want 1", got)
	}
	if !app.denylist.userBlocked(id) {
		t.Error("this server's denylist admits the user")
	}
}

// beforeGrantList is the point where a leavingStore runs an armed hook
// before a read of a user's own credential rows.
const beforeGrantList = "before grant list"

func (s *leavingStore) Credentials() store.CredentialRepo {
	return leavingCredentials{s.Store.Credentials(), s}
}

type leavingCredentials struct {
	store.CredentialRepo
	s *leavingStore
}

func (c leavingCredentials) ListByOwner(ctx context.Context, scope, owner string) ([]store.Credential, error) {
	if scope == store.CredScopeUser {
		if err := c.s.run(ctx, beforeGrantList); err != nil {
			return nil, err
		}
	}
	return c.CredentialRepo.ListByOwner(ctx, scope, owner)
}

// TestSCIMDeactivationOnAHungGrantList pins the bound of the grant removal
// that follows a clean SCIM deactivation. A store that stops answering the
// read of the user's grants holds the request only until that removal's own
// bound, auditInsertTimeout, ends the read, so the answer is the write
// bound's SCIM 503 after about adminWriteBound plus auditInsertTimeout. The
// read sees a deadline, and the kill switch before it has run: the user
// reads disabled with one user.killed record. It shrinks the bound, so it
// does not run in parallel.
func TestSCIMDeactivationOnAHungGrantList(t *testing.T) {
	old := adminWriteBound
	adminWriteBound = 300 * time.Millisecond
	t.Cleanup(func() { adminWriteBound = old })
	app, base, ls := testAppLeaving(t, slog.New(slog.DiscardHandler))
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	token := mintProvisioningToken(t, base, loginDeviceFlow(t, base, "kim", "hunter2!"))
	id := scimCreateUser(t, base, token, `{"schemas":["`+scim.SchemaUser+`"],"userName":"hang","externalId":"idm-hang","active":true}`)
	grantUserApp(t, app, "gh-hang", id)
	var bounded atomic.Bool
	ls.arm(beforeGrantList, func(ctx context.Context) error {
		_, ok := ctx.Deadline()
		bounded.Store(ok)
		return untilBound(ctx)
	})
	start := time.Now()
	code, _, out := scimSend(t, http.MethodPatch, base+"/scim/v2/Users/"+id, token,
		`{"schemas":["`+scim.SchemaPatch+`"],"Operations":[{"op":"replace","path":"active","value":false}]}`)
	took := time.Since(start)
	if detail, _ := out["detail"].(string); code != http.StatusServiceUnavailable || !strings.Contains(detail, "did not finish within") {
		t.Errorf("PATCH whose grant list hangs = %d %v, want the bound's 503", code, out)
	}
	if limit := adminWriteBound + auditInsertTimeout + 2*time.Second; took > limit {
		t.Errorf("PATCH whose grant list hangs answered after %v, want within %v", took, limit)
	}
	if !bounded.Load() {
		t.Error("the grant list ran on a context without a deadline")
	}
	if row, err := app.store.Users().GetByID(context.Background(), id); err != nil || row.Status != store.UserDisabled {
		t.Errorf("the user reads %q (%v), want disabled", row.Status, err)
	}
	if got := eventsNaming(t, app, "straza.audit.identity", "user", id); len(got) != 1 || got[0]["action"] != "user.killed" {
		t.Errorf("straza.audit.identity records of the user = %v, want one user.killed", got)
	}
}
