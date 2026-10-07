package server

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// TestSessionLifetimeJanitor pins the absolute session lifetime: a session
// older than governance.sessionMaxLifetime is closed while its token is still
// fresh, exactly one straza.audit.authn session.end with the outcome
// lifetime-closed lands for it (userId and session set, no client-connection
// fields), a younger session stays active, the closed session's token is
// refused on refresh with the stand-down sentence, and a second pass emits
// nothing more.
func TestSessionLifetimeJanitor(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(cfg *config.Config) {
		cfg.Governance.SessionMaxLifetime = time.Hour
	})
	kim := seedIdentity(t, app)
	ctx := context.Background()
	harness := map[string]string{"name": "claude-code", "version": "2.1.0"}

	oldToken, oldID := checkinToken(t, app, base)
	youngToken, youngID := checkinToken(t, app, base)
	backdateSessionStart(t, app, oldID, 2*time.Hour)

	app.closeLifetimeSessions(ctx)

	closed := waitAuthn(t, app, "session.end lifetime-closed", func(d map[string]any) bool {
		return d["action"] == "session.end" && d["outcome"] == "lifetime-closed" && d["session"] == oldID
	})
	if closed["userId"] != kim.ID {
		t.Errorf("lifetime-closed userId = %v, want %s", closed["userId"], kim.ID)
	}
	if !strings.Contains(asString(closed["reason"]), "maximum lifetime of 1h0m0s") {
		t.Errorf("lifetime-closed reason does not name the configured lifetime: %v", closed["reason"])
	}
	for _, k := range []string{"sourceIp", "userAgent"} {
		if _, present := closed[k]; present {
			t.Errorf("lifetime-closed carries %s; janitor events must not: %v", k, closed)
		}
	}
	lifetimeClosed := func(d map[string]any) bool { return d["outcome"] == "lifetime-closed" }
	if n := countAuthn(t, app, lifetimeClosed); n != 1 {
		t.Errorf("lifetime-closed events = %d, want exactly 1 (the young session must not close)", n)
	}
	for id, want := range map[string]string{oldID: store.SessionClosed, youngID: store.SessionActive} {
		ses, err := app.store.Sessions().GetByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if ses.Status != want {
			t.Errorf("session %s status = %q, want %q", id, ses.Status, want)
		}
	}

	code, body := postJSON(t, base+"/v1/checkin", map[string]any{"session_token": oldToken, "harness": harness})
	if code != http.StatusUnauthorized || body["error"] != "session is no longer active" {
		t.Errorf("refresh of the closed session = %d %v, want 401 session is no longer active", code, body)
	}
	if code, body := postJSON(t, base+"/v1/checkin", map[string]any{"session_token": youngToken, "harness": harness}); code != http.StatusOK {
		t.Errorf("refresh of the young session = %d %v, want 200", code, body)
	}

	app.closeLifetimeSessions(ctx)
	if n := countAuthn(t, app, lifetimeClosed); n != 1 {
		t.Errorf("lifetime-closed events after a second pass = %d, want still 1", n)
	}
}

// backdateSessionStart rewrites a session's started_at behind the repo's
// back, exactly as time would: Create stamps it with the store clock and no
// repo method moves it. Opens the app's sqlite file on a second connection.
func backdateSessionStart(t *testing.T, app *App, sessionID string, ago time.Duration) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(filepath.ToSlash(app.cfg.Store.DSN))+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	startedAt := time.Now().UTC().Add(-ago).Format(time.RFC3339Nano)
	if _, err := db.Exec(`UPDATE sessions SET started_at = ? WHERE id = ?`, startedAt, sessionID); err != nil {
		t.Fatalf("backdate session %s: %v", sessionID, err)
	}
}
