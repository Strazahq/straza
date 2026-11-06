package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/strazahq/straza/internal/store"
)

// revokeFault answers the error armed for one of the three writes of a user
// write that carries revoke: "users.write" for the user row, "revocations"
// for the revocation row and "outbox:<subject>" for the event. A write
// without a revocation is never failed here.
func (f *faultStore) revokeFault(revoke *store.UserRevocation) error {
	if revoke == nil {
		return nil
	}
	for _, key := range []string{"users.write", "revocations", "outbox:" + revoke.Event.Subject} {
		if err := f.fault(key); err != nil {
			return err
		}
	}
	return nil
}

// UpdateFields fails a write that carries a user revocation while one of
// its three writes is armed, before the store's transaction starts. The
// store's transaction then leaves every row as it was, which is what it does
// when the database refuses any one of the three, as
// TestUserWritesCarryTheirRevocation pins in the store. While
// "users.write.lost" is armed, such a write commits and then answers the
// armed error, as a commit whose answer the connection lost does.
func (u faultUsers) UpdateFields(ctx context.Context, id string, f store.UserFields, revoke *store.UserRevocation) (store.User, error) {
	if err := u.f.revokeFault(revoke); err != nil {
		return store.User{}, err
	}
	row, err := u.UserRepo.UpdateFields(ctx, id, f, revoke)
	if lost := u.f.fault("users.write.lost"); err == nil && revoke != nil && lost != nil {
		return store.User{}, lost
	}
	return row, err
}

// SoftDelete fails a delete that carries a user revocation the way
// UpdateFields fails a disable.
func (u faultUsers) SoftDelete(ctx context.Context, id string, revoke *store.UserRevocation) ([]store.RoleAssignment, error) {
	if err := u.f.revokeFault(revoke); err != nil {
		return nil, err
	}
	removed, err := u.UserRepo.SoftDelete(ctx, id, revoke)
	if lost := u.f.fault("users.write.lost"); err == nil && revoke != nil && lost != nil {
		return nil, lost
	}
	return removed, err
}

// closedConnErr has the shape of pgx's error for a connection it closed
// while a statement waited for its answer: it says it is safe to retry, as
// pgx's lock error does, and it unwraps to pgconn.ErrConnClosed.
type closedConnErr struct{}

func (closedConnErr) Error() string     { return "conn closed" }
func (closedConnErr) SafeToRetry() bool { return true }
func (closedConnErr) Unwrap() error     { return pgconn.ErrConnClosed }

// faultRoles fails the assignments read of the user armed as
// "roles.assignments:<user id>", so a test can fail the read a delete makes
// of its target without failing the reads that authorize the caller.
type faultRoles struct {
	store.RoleRepo
	f *faultStore
}

func (r faultRoles) ListAssignments(ctx context.Context, subjectKind, subjectID string) ([]store.RoleAssignment, error) {
	if err := r.f.fault("roles.assignments:" + subjectID); err != nil {
		return nil, err
	}
	return r.RoleRepo.ListAssignments(ctx, subjectKind, subjectID)
}

func (f *faultStore) Roles() store.RoleRepo { return faultRoles{f.Store.Roles(), f} }

// RevocationCommitted fails while "users.committed" is armed, as a store
// that cannot answer whether a write committed does.
func (u faultUsers) RevocationCommitted(ctx context.Context, userID, eventID string) (store.User, bool, error) {
	if err := u.f.fault("users.committed"); err != nil {
		return store.User{}, false, err
	}
	return u.UserRepo.RevocationCommitted(ctx, userID, eventID)
}

// TestUserDisableAndDeleteCommitWithTheirRevocation pins the admin disable
// (PATCH /v1/admin/users/{id} with status disabled) and the admin delete
// (DELETE /v1/admin/users/{id}), which write the user's change, its
// revocation row and its straza.revocation.user event together. A refused
// write answers 500 with the sentence that nothing changed, writes no row,
// event or record, leaves the session active and keeps the user on this
// server's denylist until strazactl users unlock. A write without a clear
// answer is settled by asking the store. A committed one finishes as a clean
// commit, one that did not commit says nothing changed, and one the store
// cannot settle keeps the unsure sentence. A delete whose assignments read
// fails is not sent, and a refused value answers 400 and refuses no one. A
// resend lands every record once, and a delete also removes the user's
// grant, key and approver device with one record each.
func TestUserDisableAndDeleteCommitWithTheirRevocation(t *testing.T) {
	t.Parallel()
	log, buf := captureLogger()
	app, base, fs := testAppFaultLog(t, log)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	ctx := context.Background()
	lostConn := &pgconn.PgError{Severity: "FATAL", Code: "08006", Message: "connection failure during the commit"}
	connClosed := closedConnErr{}
	refused := fmt.Errorf("%w: invalid byte sequence for encoding \"UTF8\": 0x00 (SQLSTATE 22021)", store.ErrInvalidData)

	type change struct {
		name, verb, method, reason, notDone string
		body                                any
		// answered reports whether out is the answer of a clean commit.
		answered func(id string, out map[string]any) bool
		// deprovisions marks the change that ends the user's grant, key and
		// approver device.
		deprovisions bool
		// landed reports whether the change is in the user row.
		landed func(id string) bool
		// records answers the change's own audit records and events: the
		// user.update record and straza.identity.updated of a disable, and
		// the roles.unassign record and straza.identity.deactivated of a
		// delete.
		records func(t *testing.T, id string) (audit, identity int)
	}
	changes := []change{
		{name: "disable", verb: "disable", method: http.MethodPatch, reason: "user disabled by admin",
			notDone: userNotDisabledMsg, body: map[string]string{"status": store.UserDisabled},
			answered: func(id string, out map[string]any) bool {
				return out["id"] == id && out["status"] == store.UserDisabled
			},
			landed: func(id string) bool {
				u, err := app.store.Users().GetByID(ctx, id)
				return err == nil && u.Status == store.UserDisabled
			},
			records: func(t *testing.T, id string) (int, int) {
				n := 0
				for _, rec := range eventsNaming(t, app, "straza.audit.admin", "user", id) {
					if rec["action"] == actionUserUpdate {
						n++
					}
				}
				return n, len(eventsNaming(t, app, "straza.identity.updated", "id", id))
			}},
		{name: "delete", verb: "delete", method: http.MethodDelete, reason: "user deleted by admin",
			notDone: userNotDeletedMsg, deprovisions: true,
			answered: func(_ string, out map[string]any) bool { return len(out) == 1 && out["status"] == "deleted" },
			landed: func(id string) bool {
				_, err := app.store.Users().GetByID(ctx, id)
				return errors.Is(err, store.ErrNotFound)
			},
			records: func(t *testing.T, id string) (int, int) {
				n := 0
				for _, rec := range eventsNaming(t, app, "straza.audit.admin", "user", id) {
					if rec["action"] == actionRolesUnassign {
						n++
					}
				}
				return n, len(eventsNaming(t, app, "straza.identity.deactivated", "id", id))
			}},
	}
	faults := []struct {
		name   string
		arm    []string
		err    error
		unsure bool
		// value marks a value the database refuses, which only a disable's
		// request body can carry.
		value bool
		// landed marks a write that committed before its answer was lost.
		landed bool
		// deleteOnly marks a fault in the read only a delete makes.
		deleteOnly bool
	}{
		{name: "a healthy store"},
		{name: "the user row is refused", arm: []string{"users.write"}, err: pgDown},
		{name: "the revocation row is refused", arm: []string{"revocations"}, err: pgDown},
		{name: "the event is refused", arm: []string{"outbox:straza.revocation.user"}, err: pgDown},
		{name: "a commit whose connection failed before its answer", arm: []string{"users.write.lost"}, err: lostConn, landed: true},
		{name: "a commit whose connection pgx closed before its answer", arm: []string{"users.write.lost"}, err: connClosed, landed: true},
		{name: "a write that ends without an answer and did not commit", arm: []string{"users.write"}, err: lostConn},
		{name: "a write that ends without an answer the store cannot settle", arm: []string{"users.write", "users.committed"}, err: lostConn, unsure: true},
		{name: "a value the database refuses", arm: []string{"users.write"}, err: refused, value: true},
		{name: "the user's assignments cannot be read before the delete", arm: []string{"roles.assignments:<user>"}, err: lostConn, deleteOnly: true},
	}
	// holds checks what the store and this server hold for u after n
	// changes landed, 0 or 1, with session the id of u's session, and
	// whether this server's denylist refuses u.
	holds := func(t *testing.T, c change, u store.User, session string, n int, blocked bool) {
		t.Helper()
		if got := c.landed(u.ID); got != (n == 1) {
			t.Errorf("the %s is in the user row: %v, want %v", c.verb, got, n == 1)
		}
		rows, err := app.store.Revocations().ListByTarget(ctx, store.RevokeUser, u.ID)
		if err != nil || len(rows) != n {
			t.Errorf("revocation rows = %d (%v), want %d", len(rows), err, n)
		}
		for _, rv := range rows {
			if rv.Reason != c.reason || rv.Origin != store.RevocationOriginAdmin {
				t.Errorf("revocation row = %+v, want reason %q and origin admin", rv, c.reason)
			}
		}
		if got := eventsNaming(t, app, "straza.revocation.user", "user", u.ID); len(got) != n {
			t.Errorf("straza.revocation.user events = %v, want %d", got, n)
		}
		var killed []map[string]any
		for _, rec := range eventsNaming(t, app, "straza.audit.identity", "user", u.ID) {
			if rec["action"] == "user.killed" {
				killed = append(killed, rec)
			}
		}
		if len(killed) != n {
			t.Errorf("user.killed records = %v, want %d", killed, n)
		}
		for _, rec := range killed {
			if rec["reason"] != c.reason || rec["sessionsRevoked"] != float64(1) {
				t.Errorf("user.killed record = %v, want the reason and one session", rec)
			}
		}
		if audit, identity := c.records(t, u.ID); audit != n || identity != n {
			t.Errorf("the %s's own audit records = %d and identity events = %d, want %d each", c.verb, audit, identity, n)
		}
		want := store.SessionActive
		if n == 1 {
			want = store.SessionRevoked
		}
		if ses, err := app.store.Sessions().GetByID(ctx, session); err != nil || ses.Status != want {
			t.Errorf("the user's session is %q (%v), want %s", ses.Status, err, want)
		}
		if got := app.denylist.userBlocked(u.ID); got != blocked {
			t.Errorf("this server's denylist refuses the user: %v, want %v", got, blocked)
		}
		gone := c.deprovisions && n == 1
		ended := 0
		if gone {
			ended = 1
		}
		if got := len(userGrants(t, app, u.ID)); got != 1-ended {
			t.Errorf("grant rows = %d, want %d", got, 1-ended)
		}
		if got := len(grantRemoveEvents(t, app, u.ID)); got != ended {
			t.Errorf("apps.grant.remove records = %d, want %d", got, ended)
		}
		if _, err := app.store.Settings().Get(ctx, nhiKeyPrefix+u.ID); errors.Is(err, store.ErrNotFound) != gone {
			t.Errorf("the user's key reads %v, want it gone: %v", err, gone)
		}
		if devs, err := app.store.Approvers().ListDevices(ctx, u.ID); err != nil || len(devs) != 1-ended {
			t.Errorf("approver devices = %d (%v), want %d", len(devs), err, 1-ended)
		}
		keyRecords, deviceRecords := 0, 0
		for _, rec := range adminAuditEvents(t, app) {
			switch {
			case rec["user"] != u.ID:
			case rec["action"] == actionNHIKeyRemoved:
				keyRecords++
			case rec["action"] == "approver.revoke":
				deviceRecords++
			}
		}
		if keyRecords != ended || deviceRecords != ended {
			t.Errorf("nhi-key.removed records = %d and approver.revoke records = %d, want %d each", keyRecords, deviceRecords, ended)
		}
	}
	for i, c := range changes {
		for j, fault := range faults {
			if fault.value && c.verb != "disable" || fault.deleteOnly && c.verb != "delete" {
				continue
			}
			t.Run(c.name+" with "+fault.name, func(t *testing.T) {
				name := fmt.Sprintf("leaver%d%d", i, j)
				u := mkHuman(t, app, name, AdminRole)
				session, _ := storedSession(t, app, u.ID, "", "console")
				grantUserApp(t, app, "gh-"+name, u.ID)
				enrollApprover(t, app, u.ID)
				if err := app.store.Settings().Set(ctx, nhiKeyPrefix+u.ID, `{"public_key":"a2V5"}`); err != nil {
					t.Fatal(err)
				}
				send := func() (int, string, map[string]any) {
					code, hdr, out := callJSON(t, c.method, base+"/v1/admin/users/"+u.ID, bearer, c.body)
					return code, hdr.Get(requestIDHeader), out
				}
				for _, key := range fault.arm {
					fs.arm(strings.ReplaceAll(key, "<user>", u.ID), fault.err)
				}
				code, id, out := send()
				fs.disarm()
				var tagged []string
				for _, rec := range errorRecords(buf) {
					if strings.Contains(rec, "correlation_id="+id) {
						tagged = append(tagged, rec)
					}
				}
				if fault.arm == nil || fault.landed {
					if code != http.StatusOK || !c.answered(u.ID, out) {
						t.Fatalf("%s = %d %v, want 200 and the answer of a clean commit", c.verb, code, out)
					}
					if len(tagged) != 0 {
						t.Errorf("Error records of the %s = %q, want none", c.verb, tagged)
					}
					holds(t, c, u, session, 1, true)
					return
				}
				if fault.value {
					want := fmt.Sprintf(userValueRefusedMsg, name)
					if code != http.StatusBadRequest || out["error"] != want || len(tagged) != 0 {
						t.Errorf("%s = %d %v with Error records %q, want 400 %q and none", c.verb, code, out, tagged, want)
					}
					holds(t, c, u, session, 0, false)
				} else {
					want := fmt.Sprintf(c.notDone, name, name, id)
					if fault.unsure {
						want = fmt.Sprintf(userChangeUnsureMsg, c.verb, name, c.verb, c.verb, name, id)
					}
					if code != http.StatusInternalServerError || out["error"] != want || out[correlationKey] != id {
						t.Errorf("%s = %d %v, want 500 %q under %s", c.verb, code, out, want, id)
					}
					if len(tagged) != 1 || !strings.Contains(tagged[0], fault.err.Error()) {
						t.Errorf("Error records under %s = %q, want one that names the cause", id, tagged)
					}
					holds(t, c, u, session, 0, true)
					// The sentence says strazactl users unlock lifts this
					// server's refusal, which no revocation row backs.
					if code, _, out := callJSON(t, http.MethodPost, base+"/v1/admin/users/"+u.ID+"/unlock", bearer, nil); code != http.StatusOK {
						t.Fatalf("unlock = %d %v, want 200", code, out)
					}
					if app.denylist.userBlocked(u.ID) {
						t.Error("this server's denylist still refuses the user after the unlock")
					}
				}
				if code, _, out := send(); code != http.StatusOK {
					t.Fatalf("the %s sent again = %d %v, want 200", c.verb, code, out)
				}
				holds(t, c, u, session, 1, true)
			})
		}
	}
}
