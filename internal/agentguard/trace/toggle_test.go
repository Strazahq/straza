package trace

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestToggleRoundTripAndExpiry(t *testing.T) {
	dir := stateDir(t)
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)

	if _, present, err := ReadToggle(dir); present || err != nil {
		t.Fatalf("fresh state dir: present=%v err=%v", present, err)
	}
	want := Toggle{Level: "debug", Until: now.Add(DefaultWindow)}
	if err := WriteToggle(dir, want); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(TogglePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	wantMode := os.FileMode(0o600)
	if runtime.GOOS == "windows" {
		// Windows reports writable files as 0666; FileMode does not expose ACLs.
		wantMode = 0o666
	}
	if fi.Mode().Perm() != wantMode {
		t.Errorf("toggle mode = %o, want %04o", fi.Mode().Perm(), wantMode)
	}
	got, present, err := ReadToggle(dir)
	if err != nil || !present {
		t.Fatalf("ReadToggle: present=%v err=%v", present, err)
	}
	if got.Level != "debug" || !got.Until.Equal(want.Until) {
		t.Fatalf("round trip mangled: %+v", got)
	}
	if !got.Active(now) || !got.Active(now.Add(DefaultWindow-time.Second)) {
		t.Error("toggle must be active inside its window")
	}
	if got.Active(now.Add(DefaultWindow)) || !got.Expired(now.Add(DefaultWindow)) {
		t.Error("toggle must expire at Until")
	}
	if (Toggle{}).Expired(now) {
		t.Error("an absent toggle is not expired, it is absent")
	}
	if (Toggle{Level: "journal", Until: now.Add(time.Hour)}).Active(now) {
		t.Error("only level debug activates the window")
	}
	// No temp residue beside the toggle.
	entries, _ := filepath.Glob(filepath.Join(dir, ".*tmp*"))
	if len(entries) != 0 {
		t.Errorf("temp residue left behind: %v", entries)
	}
}

func TestToggleRemoveAbsentIsNil(t *testing.T) {
	dir := stateDir(t)
	if err := RemoveToggle(dir); err != nil {
		t.Fatalf("removing an absent toggle must be nil, got %v", err)
	}
	if err := WriteToggle(dir, Toggle{Level: "debug", Until: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := RemoveToggle(dir); err != nil {
		t.Fatal(err)
	}
	if _, present, _ := ReadToggle(dir); present {
		t.Fatal("toggle still present after RemoveToggle")
	}
}

func TestReadToggleCorruptIsPresentWithError(t *testing.T) {
	dir := stateDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(TogglePath(dir), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	tg, present, err := ReadToggle(dir)
	if !present || err == nil {
		t.Fatalf("corrupt toggle: present=%v err=%v", present, err)
	}
	if tg.Active(time.Now()) {
		t.Fatal("a corrupt toggle must never activate debug")
	}
	// Resolve treats it as absent: the journal stays on, debug stays off.
	if got := Resolve("", tg, time.Now()); got != Journal {
		t.Fatalf("Resolve over a corrupt toggle = %s, want journal", got)
	}
}
