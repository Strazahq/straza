package trace

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func stateDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "state")
}

func readLinesT(t *testing.T, dir string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, ln := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}

func TestResolveLevelMatrix(t *testing.T) {
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	active := Toggle{Level: "debug", Until: now.Add(time.Hour)}
	expired := Toggle{Level: "debug", Until: now.Add(-time.Minute)}
	wrong := Toggle{Level: "journal", Until: now.Add(time.Hour)}
	cases := []struct {
		cfg  string
		tg   Toggle
		want Level
	}{
		{"", Toggle{}, Journal},
		{"", active, Debug},
		{"", expired, Journal},
		{"", wrong, Journal},
		{"journal", Toggle{}, Journal},
		{"journal", active, Debug},
		{"journal", expired, Journal},
		{"off", Toggle{}, Off},
		{"off", active, Off},
		{"off", expired, Off},
		{"debug", Toggle{}, Debug},
		{"debug", active, Debug},
		{"debug", expired, Debug},
		{"bogus", Toggle{}, Journal},
		{"bogus", active, Debug},
	}
	for _, c := range cases {
		if got := Resolve(c.cfg, c.tg, now); got != c.want {
			t.Errorf("Resolve(%q, %+v) = %s, want %s", c.cfg, c.tg, got, c.want)
		}
	}
}

func TestLoggerLevelGating(t *testing.T) {
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		cfg       string
		toggle    *Toggle
		wantLevel Level
		wantLines int // after one Journal + one Debug call
	}{
		{"default journal", "", nil, Journal, 1},
		{"toggle active", "", &Toggle{Level: "debug", Until: now.Add(time.Hour)}, Debug, 2},
		{"toggle expired", "", &Toggle{Level: "debug", Until: now.Add(-time.Second)}, Journal, 1},
		{"config off beats toggle", "off", &Toggle{Level: "debug", Until: now.Add(time.Hour)}, Off, 0},
		{"config debug forces", "debug", nil, Debug, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := stateDir(t)
			if c.toggle != nil {
				if err := WriteToggle(dir, *c.toggle); err != nil {
					t.Fatal(err)
				}
			}
			l := New(dir, c.cfg, now)
			if l.Level() != c.wantLevel {
				t.Fatalf("level = %s, want %s", l.Level(), c.wantLevel)
			}
			if l.DebugOn() != (c.wantLevel == Debug) {
				t.Fatalf("DebugOn = %v", l.DebugOn())
			}
			l.Journal("decision", slog.String("tool", "shell.exec"), slog.String("effect", "allow"))
			l.Debug("adapter", slog.String("harness", "claude-code"))
			lines := readLinesT(t, dir)
			if len(lines) != c.wantLines {
				t.Fatalf("lines = %d, want %d: %v", len(lines), c.wantLines, lines)
			}
			for _, ln := range lines {
				var rec map[string]any
				if err := json.Unmarshal([]byte(ln), &rec); err != nil {
					t.Fatalf("line not JSON: %v (%q)", err, ln)
				}
				if v, _ := rec["v"].(float64); v != Version {
					t.Errorf("v = %v, want %d", rec["v"], Version)
				}
				ts, _ := rec["ts"].(string)
				if !strings.HasSuffix(ts, "Z") {
					t.Errorf("ts not UTC: %q", ts)
				}
				if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
					t.Errorf("ts not RFC3339Nano: %q", ts)
				}
				if _, ok := rec["time"]; ok {
					t.Errorf("slog default time key leaked: %q", ln)
				}
				switch rec["msg"] {
				case "decision":
					if rec["level"] != "INFO" || rec["tool"] != "shell.exec" {
						t.Errorf("journal record mangled: %q", ln)
					}
				case "adapter":
					if rec["level"] != "DEBUG" || rec["harness"] != "claude-code" {
						t.Errorf("debug record mangled: %q", ln)
					}
				default:
					t.Errorf("unexpected record: %q", ln)
				}
			}
		})
	}
}

func TestLoggerNilSafe(t *testing.T) {
	var l *Logger
	if l.Level() != Off || l.DebugOn() {
		t.Fatal("nil logger must report Off")
	}
	l.Journal("decision")
	l.Debug("adapter")
	l.SetRecheck(time.Second)
}

func TestLoggerRecheckPicksUpToggle(t *testing.T) {
	dir := stateDir(t)
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	l := New(dir, "", now, Recheck(time.Minute))
	clock := now
	l.clock = func() time.Time { return clock }
	if l.Level() != Journal {
		t.Fatalf("initial level = %s", l.Level())
	}
	if err := WriteToggle(dir, Toggle{Level: "debug", Until: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	// Inside the recheck window: the cached level stands.
	clock = now.Add(30 * time.Second)
	if l.Level() != Journal {
		t.Fatal("level changed before the recheck interval elapsed")
	}
	clock = now.Add(61 * time.Second)
	if l.Level() != Debug {
		t.Fatal("recheck did not pick up the toggle")
	}
	// Expiry is observed on the next recheck too.
	clock = now.Add(2 * time.Hour)
	if l.Level() != Journal {
		t.Fatal("recheck did not observe the expired window")
	}
	// A hook-style logger (no recheck) keeps the level it resolved at start.
	h := New(dir, "", now)
	h.clock = func() time.Time { return now.Add(time.Hour) }
	_ = RemoveToggle(dir)
	if h.Level() != Debug {
		t.Fatal("recheck 0 must keep the level resolved at construction")
	}
	h.SetRecheck(time.Nanosecond)
	if h.Level() != Journal {
		t.Fatal("SetRecheck must enable re-resolution")
	}
}

func TestDurationString(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{time.Hour, "1h"},
		{30 * time.Minute, "30m"},
		{90 * time.Minute, "1h30m"},
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m30s"},
		{24 * time.Hour, "24h"},
		{0, "0s"},
		{1500 * time.Millisecond, "2s"},
		{-time.Hour, "-1h"},
	}
	for _, c := range cases {
		if got := DurationString(c.in); got != c.want {
			t.Errorf("DurationString(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestShortBounds(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"abc", "abc"},
		{"abcdef", "abc..."},
		{"héllo", "hé..."}, // cut lands on a rune boundary, never inside é
	}
	for _, c := range cases {
		if got := Short(c.in, 3); got != c.want {
			t.Errorf("Short(%q, 3) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := Short("abc", 0); got != "..." {
		t.Errorf("Short with n=0 = %q", got)
	}
}
