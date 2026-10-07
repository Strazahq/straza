package server

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

func postBatch(t *testing.T, base, token string) *http.Response {
	t.Helper()
	body := []byte(`{"events":[{"specversion":"1.0","id":"bp-` + t.Name() + `","type":"straza.audit.tool","source":"straza","data":{"command":"ls"}}]}`)
	req, err := http.NewRequest(http.MethodPost, base+"/v1/audit/batch", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestAuditIngestRateLimit pins the per-session ingest bucket: a burst over the
// configured RPS answers 429 + Retry-After, and nothing is lost server-side
// to enforce it (the client spool retries).
func TestAuditIngestRateLimit(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(c *config.Config) {
		c.Governance.AuditIngestPerSessionRPS = 1 // burst = 1: second call trips
	})
	seedIdentity(t, app)
	token, _ := checkinToken(t, app, base)

	if resp := postBatch(t, base, token); resp.StatusCode != http.StatusOK {
		t.Fatalf("first batch = %d, want 200", resp.StatusCode)
	}
	resp := postBatch(t, base, token)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("burst batch = %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}
}

// TestAuditIngestBacklogGate pins the ingest depth gate: with the outbox backlog
// at the limit, ingest answers 429 until the relay drains it.
func TestAuditIngestBacklogGate(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(c *config.Config) {
		c.Governance.AuditIngestBacklogLimit = 5
		c.Governance.AuditIngestPerSessionRPS = 0 // isolate the depth gate
	})
	seedIdentity(t, app)
	token, _ := checkinToken(t, app, base)

	// Fill the outbox past the limit with rows on a subject the relay is
	// not asked to publish yet (insert directly; the relay may drain them,
	// so assert against the cached gate immediately after seeding).
	ctx := context.Background()
	for i := 0; i < 6; i++ {
		if _, err := app.store.Outbox().Insert(ctx, store.OutboxEvent{
			Subject: "straza.audit.tool", CE: `{"id":"backlog-seed"}`,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if !app.auditBacklogOver(ctx) {
		t.Fatal("backlog gate not tripped at 6 rows with limit 5")
	}
	if resp := postBatch(t, base, token); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("batch over backlog = %d, want 429", resp.StatusCode)
	}
}
