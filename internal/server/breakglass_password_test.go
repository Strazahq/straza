package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/events"
	"github.com/strazahq/straza/internal/store"
)

// hostSource matches the source of a record the host command writes, the
// form strazad serve gives its own records.
var hostSource = regexp.MustCompile(`^strazad/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// hostRecordData is the data of the record SetBreakGlassPassword writes for
// the break-glass row id, as JSON decodes it.
func hostRecordData(id string) map[string]any {
	return map[string]any{
		"action": "user.update", "target": id, "user": id, "username": BreakGlassUsername,
		"changed": []any{"password"}, "reason": "set on the server host with strazad break-glass set-password",
	}
}

// TestBreakGlassHostPasswordRecord pins the record SetBreakGlassPassword
// writes with the hash: exactly one unpublished straza.audit.admin row
// whose envelope and data are the spec/events revision 73 shape, with no
// actor field, which the envelope validator accepts, and whose bytes carry
// neither the password nor its hash.
func TestBreakGlassHostPasswordRecord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := bootStore(t)
	if err := bootAppAs(st, slog.New(slog.DiscardHandler), config.ProfileEnterprise).ensureBreakGlass(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := st.Outbox().ListRecent(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	password, err := SetBreakGlassPassword(ctx, st.Users())
	if err != nil {
		t.Fatalf("SetBreakGlassPassword: %v", err)
	}
	after, err := st.Outbox().ListRecent(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before)+1 {
		t.Fatalf("outbox rows = %d after, %d before, want exactly one more", len(after), len(before))
	}
	row := after[0]
	bg := assertBreakGlassOnce(t, st)
	if row.Subject != "straza.audit.admin" || row.Published {
		t.Errorf("row subject %q published %v, want an unpublished straza.audit.admin", row.Subject, row.Published)
	}
	if strings.Contains(row.CE, password) || strings.Contains(row.CE, bg.PasswordHash) {
		t.Errorf("the record carries the password or its hash: %s", row.CE)
	}
	var ce map[string]any
	if err := json.Unmarshal([]byte(row.CE), &ce); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(ce))
	for k := range ce {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := []string{"data", "id", "source", "specversion", "time", "type"}; !slices.Equal(keys, want) {
		t.Errorf("envelope keys = %v, want %v", keys, want)
	}
	source, _ := ce["source"].(string)
	id, _ := ce["id"].(string)
	stamp, _ := ce["time"].(string)
	if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil || ce["specversion"] != "1.0" || ce["type"] != "straza.audit.admin" || id == "" || !hostSource.MatchString(source) {
		t.Errorf("envelope = %v, want specversion 1.0, type straza.audit.admin, an id, a time and a strazad/<uuid> source", ce)
	}
	if !reflect.DeepEqual(ce["data"], hostRecordData(bg.ID)) {
		t.Errorf("data = %v, want %v", ce["data"], hostRecordData(bg.ID))
	}
	if err := events.Validate([]byte(row.CE)); err != nil {
		t.Errorf("the record fails the envelope validator: %v", err)
	}
}

// refusingUsers is a user repo whose SetPasswordHash fails as a database
// that refuses the transaction does, writing nothing.
type refusingUsers struct{ store.UserRepo }

func (refusingUsers) SetPasswordHash(context.Context, string, string, store.OutboxEvent) error {
	return errors.New("database is locked")
}

// TestBreakGlassHostPasswordRefused pins the answer to a write the database
// refuses: the sentence says what failed, that nothing changed and what to
// do, carries the cause, and no password is answered.
func TestBreakGlassHostPasswordRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := bootStore(t)
	if err := bootAppAs(st, slog.New(slog.DiscardHandler), config.ProfileEnterprise).ensureBreakGlass(ctx); err != nil {
		t.Fatal(err)
	}
	password, err := SetBreakGlassPassword(ctx, refusingUsers{st.Users()})
	const want = "the database refused the new break-glass password and its audit record (database is locked), so nothing changed. " +
		"Check that the database is reachable with the settings strazad serve runs with, then run the command again"
	if err == nil || err.Error() != want || password != "" {
		t.Fatalf("SetBreakGlassPassword = %q, %v, want no password and %q", password, err, want)
	}
}

// TestBreakGlassHostPasswordChained pins that a running strazad chains the
// record the host command wrote straight into the outbox under a source
// that is not its own: its relay drains the row on its own tick, and the
// audit chain gains exactly one user.update record with the host reason
// and no actor field.
func TestBreakGlassHostPasswordChained(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	app, _ := testApp(t, func(cfg *config.Config) { cfg.Profile = config.ProfileEnterprise })
	bg, err := app.store.Users().GetByUsername(ctx, BreakGlassUsername)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SetBreakGlassPassword(ctx, app.store.Users()); err != nil {
		t.Fatalf("SetBreakGlassPassword: %v", err)
	}
	chained := func() []map[string]any {
		recs, err := app.store.Audit().List(ctx, 0, 1000)
		if err != nil {
			t.Fatal(err)
		}
		var out []map[string]any
		for _, r := range recs {
			var ce struct {
				Type   string         `json:"type"`
				Source string         `json:"source"`
				Data   map[string]any `json:"data"`
			}
			if json.Unmarshal([]byte(r.CE), &ce) == nil && ce.Type == "straza.audit.admin" && ce.Data["reason"] == reasonHostSetPassword {
				if ce.Source == app.instance {
					t.Errorf("the host record carries the running server's source %s", ce.Source)
				}
				out = append(out, ce.Data)
			}
		}
		return out
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(chained()) == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	// One more relay tick, so a second copy would have landed too.
	time.Sleep(1500 * time.Millisecond)
	if got := chained(); len(got) != 1 || !reflect.DeepEqual(got[0], hostRecordData(bg.ID)) {
		t.Fatalf("chained host records = %v, want exactly one with %v", got, hostRecordData(bg.ID))
	}
}

// loggedPassword matches the password field of a slog text line.
var loggedPassword = regexp.MustCompile(` password=(\S+)`)

// TestBreakGlassFirstBootByProfile pins the break-glass password at boot on
// each profile. Standalone stores a hash and prints the password it hashed
// once. Enterprise stores no hash, logs no password field, and says once on
// every boot that the emergency sign-in is closed, until a password is set.
// No boot after SetBreakGlassPassword logs the password it generated.
func TestBreakGlassFirstBootByProfile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		profile string
		printed int // creation lines that carry the password, on the first boot
		closed  int // closed-sign-in lines on each boot while the hash is empty
	}{
		{profile: config.ProfileStandalone, printed: 1, closed: 0},
		{profile: config.ProfileEnterprise, printed: 0, closed: 1},
	}
	for _, tc := range cases {
		t.Run(tc.profile, func(t *testing.T) {
			t.Parallel()
			st := bootStore(t)
			boot := func() string {
				logger, logs := captureLogger()
				if err := bootAppAs(st, logger, tc.profile).ensureBreakGlass(ctx); err != nil {
					t.Fatalf("ensureBreakGlass: %v", err)
				}
				return logs.String()
			}
			first := boot()
			bg := assertBreakGlassOnce(t, st)
			printed, closed := bootLines(t, first, tc.profile)
			if printed != tc.printed || closed != tc.closed {
				t.Fatalf("first boot: password lines = %d, closed lines = %d, want %d and %d:\n%s", printed, closed, tc.printed, tc.closed, first)
			}
			if tc.printed == 1 {
				m := loggedPassword.FindStringSubmatch(first)
				if m == nil || bcrypt.CompareHashAndPassword([]byte(bg.PasswordHash), []byte(m[1])) != nil {
					t.Errorf("the printed password does not open the stored hash:\n%s", first)
				}
			} else if bg.PasswordHash != "" || strings.Contains(first, "$2") {
				t.Errorf("an enterprise first boot stored hash %q or logged one:\n%s", bg.PasswordHash, first)
			}
			second := boot()
			if printed, closed := bootLines(t, second, tc.profile); printed != 0 || closed != tc.closed {
				t.Errorf("second boot: password lines = %d, closed lines = %d, want 0 and %d:\n%s", printed, closed, tc.closed, second)
			}
			password, err := SetBreakGlassPassword(ctx, st.Users())
			if err != nil {
				t.Fatalf("SetBreakGlassPassword: %v", err)
			}
			third := boot()
			if printed, closed := bootLines(t, third, tc.profile); printed != 0 || closed != 0 {
				t.Errorf("a boot after a password was set: password lines = %d, closed lines = %d, want none:\n%s", printed, closed, third)
			}
			if strings.Contains(first+second+third, password) {
				t.Errorf("a boot logged the password SetBreakGlassPassword generated")
			}
			if bg := assertBreakGlassOnce(t, st); bcrypt.CompareHashAndPassword([]byte(bg.PasswordHash), []byte(password)) != nil {
				t.Errorf("a boot replaced the password that was set")
			}
		})
	}
}

// TestEnterpriseBreakGlassPasswordJourney walks the enterprise break-glass
// admin from its first boot. The emergency sign-in refuses it while it has
// no password. The first admin, who came in through oidc.bootstrapAdmin,
// sets one over the admin API, and it signs in. SetBreakGlassPassword, the
// write of strazad break-glass set-password, then replaces that password:
// the old one is refused and the generated one signs in, with no restart.
func TestEnterpriseBreakGlassPasswordJourney(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	idpApp, idpBase := testApp(t)
	seedIdentity(t, idpApp)
	platform, base := testApp(t, func(cfg *config.Config) {
		cfg.Profile = config.ProfileEnterprise
		cfg.OIDC.Issuer = idpBase
		cfg.OIDC.ClientID = "straza"
		cfg.OIDC.BootstrapAdmin = "kim"
	})
	bg, err := platform.store.Users().GetByUsername(ctx, BreakGlassUsername)
	if err != nil {
		t.Fatal(err)
	}
	if bg.PasswordHash != "" {
		t.Fatalf("the enterprise first boot stored a break-glass hash")
	}
	refused := func(t *testing.T, password string) {
		t.Helper()
		if page := submitEmergencyLogin(t, base, BreakGlassUsername, password); !strings.Contains(page, "Invalid username or password.") {
			t.Fatalf("the emergency sign-in with %q was not refused: %s", password, page)
		}
	}
	for _, password := range []string{"", "guess"} {
		refused(t, password)
	}

	code, checkin := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token":    loginDeviceFlow(t, idpBase, "kim", "hunter2!"),
		"harness":     map[string]string{"name": "console", "version": "dev"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
	})
	admin, _ := checkin["session_token"].(string)
	if code != http.StatusOK || admin == "" {
		t.Fatalf("the first admin's checkin = %d %v", code, checkin)
	}
	if code, _, out := callJSON(t, http.MethodPatch, base+"/v1/admin/users/"+bg.ID, admin, map[string]string{"password": "vaulted-by-admin"}); code != http.StatusOK {
		t.Fatalf("the admin's set-password = %d %v", code, out)
	}
	loginDeviceFlow(t, base, BreakGlassUsername, "vaulted-by-admin")

	password, err := SetBreakGlassPassword(ctx, platform.store.Users())
	if err != nil {
		t.Fatalf("SetBreakGlassPassword: %v", err)
	}
	refused(t, "vaulted-by-admin")
	loginDeviceFlow(t, base, BreakGlassUsername, password)
}
