package agentguard

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/trace"
)

func TestStoreTraceIsLazyAndCached(t *testing.T) {
	var nilStore *Store
	if nilStore.Trace() != nil {
		t.Fatal("nil store must yield a nil logger")
	}
	store := testErrorStore(t)
	l1 := store.Trace()
	if l1 == nil {
		t.Fatal("Trace() returned nil for a live store")
	}
	if l2 := store.Trace(); l2 != l1 {
		t.Fatal("Trace() must cache the logger for the process lifetime")
	}
	if l1.Level() != trace.Journal {
		t.Fatalf("un-enrolled store level = %s, want journal", l1.Level())
	}
	// A toggle written AFTER construction is not seen: hooks resolve once per
	// process (long-lived callers opt into SetRecheck).
	if err := trace.WriteToggle(store.stateDir(), trace.Toggle{Level: "debug", Until: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if store.Trace().Level() != trace.Journal {
		t.Fatal("a once-resolved logger must keep its level")
	}
	if store.TracePath() != filepath.Join(store.stateDir(), trace.FileName) {
		t.Fatalf("TracePath = %s", store.TracePath())
	}
}

func TestEnableTraceBounds(t *testing.T) {
	store := testErrorStore(t)
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	for _, d := range []time.Duration{0, 500 * time.Millisecond, 25 * time.Hour, -time.Hour} {
		if _, err := EnableTrace(store, d, now); err == nil {
			t.Errorf("EnableTrace(%v) must refuse", d)
		}
		if _, present, _ := trace.ReadToggle(store.stateDir()); present {
			t.Fatalf("a refused window must write no toggle (%v)", d)
		}
	}
	st, err := EnableTrace(store, trace.DefaultWindow, now)
	if err != nil {
		t.Fatal(err)
	}
	if !st.TogglePresent || !st.Toggle.Until.Equal(now.Add(time.Hour)) || st.Level != trace.Debug {
		t.Fatalf("state after enable = %+v", st)
	}
	tg, present, err := trace.ReadToggle(store.stateDir())
	if err != nil || !present || !tg.Active(now) {
		t.Fatalf("toggle not written: %+v present=%v err=%v", tg, present, err)
	}
	if err := DisableTrace(store); err != nil {
		t.Fatal(err)
	}
	if err := DisableTrace(store); err != nil {
		t.Fatal("DisableTrace must be idempotent")
	}
	if _, present, _ := trace.ReadToggle(store.stateDir()); present {
		t.Fatal("toggle survived DisableTrace")
	}
}

func writeManagedConfig(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("STRAZA_SYSTEM", root)
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return ManagedConfigPath()
}

func TestEnableTraceRefusedByConfigOff(t *testing.T) {
	now := time.Now()
	t.Run("user config", func(t *testing.T) {
		store := testErrorStore(t)
		if err := store.SaveConfig(Config{ServerURL: "http://x", Trace: "off"}); err != nil {
			t.Fatal(err)
		}
		_, err := EnableTrace(store, time.Hour, now)
		if err == nil || !strings.Contains(err.Error(), "trace: off") || !strings.Contains(err.Error(), store.configPath()) {
			t.Fatalf("refusal must name the deciding file and the setting, got %v", err)
		}
		if _, present, _ := trace.ReadToggle(store.stateDir()); present {
			t.Fatal("refused enable must write no toggle")
		}
		if st := TraceStatus(store, now); st.ConfigPath != store.configPath() || st.Level != trace.Off {
			t.Fatalf("status = %+v", st)
		}
	})
	t.Run("managed config", func(t *testing.T) {
		store := testErrorStore(t)
		managed := writeManagedConfig(t, "serverUrl: http://x\ntrace: off\n")
		// The user's own file says nothing; the managed file decides.
		if err := store.SaveConfig(Config{ServerURL: "http://x"}); err != nil {
			t.Fatal(err)
		}
		_, err := EnableTrace(store, time.Hour, now)
		if err == nil || !strings.Contains(err.Error(), managed) {
			t.Fatalf("refusal must name the managed file %s, got %v", managed, err)
		}
		if st := TraceStatus(store, now); st.ConfigPath != managed || st.ConfigLevel != "off" {
			t.Fatalf("status = %+v", st)
		}
	})
}

func TestManagedOffBeatsUserOn(t *testing.T) {
	store := testErrorStore(t)
	writeManagedConfig(t, "serverUrl: http://x\ntrace: off\n")
	if err := trace.WriteToggle(store.stateDir(), trace.Toggle{Level: "debug", Until: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	l := store.Trace()
	if l.Level() != trace.Off {
		t.Fatalf("level = %s, want off", l.Level())
	}
	l.Journal("decision", slog.String("tool", "shell.exec"))
	l.Debug("adapter")
	if _, err := os.Stat(store.TracePath()); !os.IsNotExist(err) {
		t.Fatalf("trace file must not exist under managed off: %v", err)
	}
	// The error log is unaffected: errors are logged regardless.
	store.logClientError(ClientError{Kind: "hook", Err: "still recorded"})
	if errs, _ := ClientErrors(store); len(errs) != 1 {
		t.Fatalf("error log must keep writing under trace: off, got %d records", len(errs))
	}
}

func TestConfigDebugForcesDebug(t *testing.T) {
	store := testErrorStore(t)
	if err := store.SaveConfig(Config{ServerURL: "http://x", Trace: "debug"}); err != nil {
		t.Fatal(err)
	}
	if store.Trace().Level() != trace.Debug {
		t.Fatalf("level = %s, want debug", store.Trace().Level())
	}
	st, err := EnableTrace(store, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if st.ConfigLevel != "debug" || st.TogglePresent {
		t.Fatalf("forced debug must write no toggle: %+v", st)
	}
	if _, present, _ := trace.ReadToggle(store.stateDir()); present {
		t.Fatal("toggle written under forced debug")
	}
}

func TestTraceCheckStates(t *testing.T) {
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		prepare    func(t *testing.T, store *Store)
		wantStatus string
		wantDetail []string
		wantHint   bool
	}{
		{"empty", func(*testing.T, *Store) {}, checkOK, []string{"journal on, 0 records", "debug off"}, false},
		{"journal with records", func(t *testing.T, store *Store) {
			store.Trace().Journal("decision", slog.String("tool", "shell.exec"), slog.String("effect", "deny"), slog.String("rule", "no-rm-rf"))
		}, checkOK, []string{"journal 1 record(s) (last: ", "shell.exec deny no-rm-rf)", "debug off"}, false},
		{"window active", func(t *testing.T, store *Store) {
			if _, err := EnableTrace(store, time.Hour, now); err != nil {
				t.Fatal(err)
			}
		}, checkOK, []string{"debug on until 2026-08-22T11:00:00Z (1h left)"}, false},
		{"window expired", func(t *testing.T, store *Store) {
			if err := trace.WriteToggle(store.stateDir(), trace.Toggle{Level: "debug", Until: now.Add(-time.Hour)}); err != nil {
				t.Fatal(err)
			}
		}, checkWarn, []string{"debug expired 1h ago", "file"}, true},
		{"config off", func(t *testing.T, store *Store) {
			if err := store.SaveConfig(Config{ServerURL: "http://x", Trace: "off"}); err != nil {
				t.Fatal(err)
			}
		}, checkOK, []string{"journal off (", "config.yaml: trace: off)"}, false},
		{"config debug", func(t *testing.T, store *Store) {
			if err := store.SaveConfig(Config{ServerURL: "http://x", Trace: "debug"}); err != nil {
				t.Fatal(err)
			}
		}, checkOK, []string{"debug forced on by "}, false},
		{"config unknown", func(t *testing.T, store *Store) {
			if err := store.SaveConfig(Config{ServerURL: "http://x", Trace: "loud"}); err != nil {
				t.Fatal(err)
			}
		}, checkWarn, []string{`trace: "loud"`, "not off, journal or debug"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := testErrorStore(t)
			c.prepare(t, store)
			got := traceCheck(store, now)
			if got.Name != "trace" || got.Status != c.wantStatus {
				t.Fatalf("check = %+v, want status %s", got, c.wantStatus)
			}
			for _, want := range c.wantDetail {
				if !strings.Contains(got.Detail, want) {
					t.Errorf("detail %q lacks %q", got.Detail, want)
				}
			}
			if (got.Hint != "") != c.wantHint {
				t.Errorf("hint = %q, wantHint=%v", got.Hint, c.wantHint)
			}
		})
	}
}

func TestLocalChecksIncludeTrace(t *testing.T) {
	store := testErrorStore(t)
	index := func(checks []Check, name string) int {
		for i, c := range checks {
			if c.Name == name {
				return i
			}
		}
		return -1
	}
	checks := localChecks(store, Session{}, false)
	ti, si := index(checks, "trace"), index(checks, "audit-spool")
	if ti < 0 || si < 0 || ti < si {
		t.Fatalf("trace row must follow audit-spool: trace=%d audit-spool=%d (%v)", ti, si, names(checks))
	}
	store.logClientError(ClientError{Kind: "hook", Err: "boom"})
	checks = localChecks(store, Session{}, false)
	ti, ci := index(checks, "trace"), index(checks, "client-errors")
	if ci < 0 || ti < ci {
		t.Fatalf("trace row must follow client-errors: trace=%d client-errors=%d (%v)", ti, ci, names(checks))
	}
}

func names(checks []Check) []string {
	out := make([]string, 0, len(checks))
	for _, c := range checks {
		out = append(out, c.Name)
	}
	return out
}
