package server

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"

	"github.com/strazahq/straza/internal/config"
)

// Fault injection: outages mid-traffic must degrade exactly as the
// invariants promise. Request paths keep deciding from memory (no DB reads
// on request paths), audit stays async and never blocks a call, and
// recovery loses nothing that reached the outbox.

// startFaultNATS runs a listener NATS on a caller-chosen port so the test
// can kill it and bring it back at the same address (strazad's client
// reconnects forever).
func startFaultNATS(t *testing.T, port int, storeDir string) *natsserver.Server {
	t.Helper()
	ns, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: port,
		JetStream: true, StoreDir: storeDir,
		NoLog: true, NoSigs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats not ready")
	}
	return ns
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// TestNATSOutageMidTraffic kills the event spine under live traffic:
// decisions keep flowing (a tool call never blocks on audit I/O), the
// outbox retains every event, and when NATS comes back the audit chain
// catches up: zero loss end to end.
func TestNATSOutageMidTraffic(t *testing.T) {
	// Serial: it restarts NATS on a port it picked and closed, which a parallel boot could take.
	port, storeDir := freePort(t), t.TempDir()
	ns := startFaultNATS(t, port, storeDir)

	app, base := testApp(t, func(cfg *config.Config) {
		cfg.Events = config.Events{Embedded: false, URL: "nats://127.0.0.1:" + strconv.Itoa(port)}
	})
	seedIdentity(t, app)
	tok := sessionToken(t, base, "kim")
	ctx := context.Background()

	// Baseline: one decision while healthy, and wait for its audit record so
	// the pre-outage pipeline is proven live.
	if code, _ := decide(t, base, tok, map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "git status"}); code != http.StatusOK {
		t.Fatalf("healthy decide = %d", code)
	}
	waitForChain(t, app, 1, "pre-outage audit")

	last, err := app.store.Audit().Last(ctx)
	if err != nil {
		t.Fatal(err)
	}
	baseline := last.Seq

	// --- Outage. ---
	ns.Shutdown()
	ns.WaitForShutdown()

	const during = 5
	for i := 0; i < during; i++ {
		code, body := decide(t, base, tok, map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "git log"})
		if code != http.StatusOK {
			t.Fatalf("decide during NATS outage = %d %v; a dead spine must not block decisions (audit is async)", code, body)
		}
	}
	// The gateway also answers (empty catalog is a valid answer; the point is
	// no hang and no 5xx).
	if code, _, _ := mcpCall(t, base, tok, "tools/list", nil); code != http.StatusOK {
		t.Fatalf("gateway during NATS outage = %d", code)
	}

	// --- Recovery: same port, same JetStream store. ---
	ns2 := startFaultNATS(t, port, storeDir)
	t.Cleanup(ns2.Shutdown)

	// Every outage-time decision must reach the tamper-evident chain: the
	// outbox held them, the relay re-publishes, the consumer appends.
	deadline := time.Now().Add(30 * time.Second)
	for {
		last, err := app.store.Audit().Last(ctx)
		if err == nil && last.Seq >= baseline+during {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("audit chain did not catch up after NATS recovery: seq=%d want>=%d", last.Seq, baseline+during)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestStoreOutageMidTraffic severs the database under live traffic: the
// decision and gateway paths read nothing from it so they keep
// answering (including the deny path with its reason) while the admin
// plane fails loudly. Audit accepted during the outage is dropped with a
// counter (the configured drop backpressure), never blocking a call.
func TestStoreOutageMidTraffic(t *testing.T) {
	t.Parallel()
	app, base := testApp(t) // fresh standalone: the starter policy is active
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	adminBearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	tok := sessionToken(t, base, "kim")

	// Healthy warm-up: allow and deny both work.
	if code, _ := decide(t, base, tok, map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "git status"}); code != http.StatusOK {
		t.Fatalf("healthy decide = %d", code)
	}

	// --- Outage: close the store out from under the running server. ---
	if err := app.store.Close(); err != nil {
		t.Logf("store close: %v", err)
	}

	for i := 0; i < 5; i++ {
		code, body := decide(t, base, tok, map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "git status"})
		if code != http.StatusOK {
			t.Fatalf("decide during store outage = %d %v; the decision path must not touch the DB", code, body)
		}
		if body["effect"] != "allow" {
			t.Fatalf("allow decision wrong during outage: %v", body)
		}
	}
	// The deny path (compiled snapshot, in memory) still carries its reason.
	code, body := decide(t, base, tok, map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "rm -rf /tmp/x"})
	if code != http.StatusOK || body["effect"] != "deny" || body["reason"] == "" {
		t.Fatalf("deny during store outage = %d %v", code, body)
	}
	// Gateway: token verify + catalog are in-memory too.
	if code, _, _ := mcpCall(t, base, tok, "tools/list", nil); code != http.StatusOK {
		t.Fatalf("gateway during store outage = %d", code)
	}
	// The admin plane fails closed, never open: with the store down even the
	// admin's own identity can't be established, so the answer is a refusal
	// (401: unknown state = deny, fail closed) or a loud 5xx. Never a 2xx.
	var out any
	if code := adminReq(t, "GET", base+"/v1/admin/users", adminBearer, nil, &out); code < 400 {
		t.Fatalf("admin list during store outage = %d, want a refusal", code)
	}
}

// waitForChain waits until the audit chain holds at least n records.
func waitForChain(t *testing.T, app *App, n int64, what string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if last, err := app.store.Audit().Last(context.Background()); err == nil && last.Seq >= n {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s (chain < %d records)", what, n)
}
