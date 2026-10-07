package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/strazahq/straza/internal/config"
)

// TestPolicyActivationPushesNudge pins the policy-update push (spec/events
// §5 rev 7): activating a policy through the admin API
// publishes ONE core-NATS nudge on the broadcast subject with the new
// snapshot id, and every checkin advertises that subject to daemons.
func TestPolicyActivationPushesNudge(t *testing.T) {
	t.Parallel()
	ns, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1,
		JetStream: true, StoreDir: t.TempDir(),
		NoLog: true, NoSigs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ns.Start()
	t.Cleanup(ns.Shutdown)
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats not ready")
	}

	app, base := testApp(t, func(cfg *config.Config) {
		cfg.Events = config.Events{Embedded: false, URL: ns.ClientURL()}
		cfg.Store.DSN = filepath.Join(t.TempDir(), "nudge.db")
	})
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	adminTok, _ := checkinToken(t, app, base)

	// The per-pod straza.push.> subscription behind GET /v1/push is what
	// hears this subject; the test
	// listens on the broker directly to pin the emitted nudge itself.
	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	nudges := make(chan *nats.Msg, 4)
	if _, err := nc.ChanSubscribe(policyPushSubject, nudges); err != nil {
		t.Fatal(err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}

	const doc = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: nudge-me }
spec:
  rules:
    - {id: r1, tools: [shell.exec], command: {denyPatterns: ["rm -rf *"]}, effect: deny, reason: "no"}
`
	req, _ := http.NewRequest(http.MethodPut, base+"/v1/admin/policies", strings.NewReader(doc))
	req.Header.Set("Authorization", "Bearer "+adminTok)
	req.Header.Set("Content-Type", "application/yaml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("apply = %d", resp.StatusCode)
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/policies/nudge-me/activate", adminTok,
		map[string]string{"status": "active"}, nil); code != http.StatusOK {
		t.Fatalf("activate = %d", code)
	}

	select {
	case msg := <-nudges:
		var body struct {
			Kind     string `json:"kind"`
			Snapshot string `json:"snapshot"`
		}
		if err := json.Unmarshal(msg.Data, &body); err != nil {
			t.Fatalf("nudge payload %q: %v", msg.Data, err)
		}
		if body.Kind != "policy" || body.Snapshot != app.snapshots.Current().ID {
			t.Errorf("nudge = %+v, want kind=policy snapshot=%s", body, app.snapshots.Current().ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no policy nudge within 5 s of activation")
	}
}
