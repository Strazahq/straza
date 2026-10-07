package agentguard

import (
	"context"
	"strings"
	"testing"
)

// TestDoctorNamesHeadlessLane pins the identity row for headless
// enrollments: a healthy deviceless NHI box names its credential lane
// instead of warning about a device credential it will never have.
func TestDoctorNamesHeadlessLane(t *testing.T) {
	t.Setenv("STRAZA_HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConfig(Config{ServerURL: "http://127.0.0.1:1", SnapshotKeys: map[string]string{"k1": "x"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveIdentity(Identity{Username: "ci-bot", Headless: HeadlessKey}); err != nil {
		t.Fatal(err)
	}

	var identity *Check
	for _, c := range Doctor(context.Background(), store) {
		if c.Name == "identity" {
			identity = &c
			break
		}
	}
	if identity == nil {
		t.Fatal("no identity check emitted")
	}
	if identity.Status != checkOK {
		t.Errorf("identity = %+v, want OK for a headless enrollment", *identity)
	}
	if !strings.Contains(identity.Detail, HeadlessKey) {
		t.Errorf("identity detail %q does not name the headless lane", identity.Detail)
	}
	if strings.Contains(identity.Detail, "enrolled before device credentials existed") {
		t.Errorf("healthy headless box warned about device credentials: %q", identity.Detail)
	}
}
