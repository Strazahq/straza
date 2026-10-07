package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// probeCaptureIngest measures conversation capture ingest. This probe
// drives the REAL ingest surface (spool-shaped batches of capture CEs into
// /v1/audit/batch) as fast as the server absorbs them, then measures how
// long the spine takes to drain the burst into BOTH read models: the hash
// chain and conversation_turns. Zero loss is asserted. The rate and drain
// budgets are CI-scale regression tripwires, not reference numbers.
func probeCaptureIngest(ctx context.Context, events int) Probe {
	const name = "capture-ingest"
	w, err := bootStrazad(ctx, strazadOpts{dataDir: tempDir("capture")})
	if err != nil {
		return failed(name, err)
	}
	defer w.stop()

	users, err := w.seedUsers(ctx, 1, "capture-role")
	if err != nil {
		return failed(name, err)
	}
	idTok, err := w.idToken(users[0])
	if err != nil {
		return failed(name, err)
	}
	client := newLoadClient(8)
	sessionID, sessionToken, err := w.checkin(client, idTok)
	if err != nil {
		return failed(name, err)
	}

	// Baselines: checkin itself emits audit events that will chain.
	chainBase, turnsBase, err := captureCounts(ctx, w, sessionID, events)
	if err != nil {
		return failed(name, err)
	}

	// Ingest: 50-record batches of alternating prompt/reply CEs with ~2 KiB
	// content (the capacity model's typical turn).
	content := strings.Repeat("x", 2<<10)
	const batchSize = 50
	sent := 0
	start := time.Now()
	for sent < events {
		n := min(batchSize, events-sent)
		var evs []json.RawMessage
		for i := 0; i < n; i++ {
			kind := "prompt"
			if (sent+i)%2 == 1 {
				kind = "reply"
			}
			ce := fmt.Sprintf(`{"specversion":"1.0","id":"cap-%06d","type":"straza.audit.%s","source":"straza","time":%q,"data":{"content":%q,"mode":"verbatim","truncated":false,"contentHash":"sha256:probe"}}`,
				sent+i, kind, time.Now().UTC().Format(time.RFC3339Nano), content)
			evs = append(evs, json.RawMessage(ce))
		}
		body, _ := json.Marshal(map[string]any{"events": evs})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.base+"/v1/audit/batch", bytes.NewReader(body))
		if err != nil {
			return failed(name, err)
		}
		req.Header.Set("Authorization", "Bearer "+sessionToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return failed(name, err)
		}
		var out struct {
			Accepted int `json:"accepted"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || out.Accepted != n {
			return failed(name, fmt.Errorf("batch at %d: HTTP %d accepted %d/%d", sent, resp.StatusCode, out.Accepted, n))
		}
		sent += n
	}
	ingest := time.Since(start)
	rate := float64(sent) / ingest.Seconds()

	// Drain: both read models must absorb the whole burst.
	drainStart := time.Now()
	deadline := drainStart.Add(60 * time.Second)
	var drain time.Duration
	for {
		chainSeq, turns, err := captureCounts(ctx, w, sessionID, events)
		if err != nil {
			return failed(name, err)
		}
		if chainSeq-chainBase >= int64(sent) && turns-turnsBase >= sent {
			drain = time.Since(drainStart)
			break
		}
		if time.Now().After(deadline) {
			return failed(name, fmt.Errorf("drain incomplete after 60s: chain +%d/%d, turns +%d/%d",
				chainSeq-chainBase, sent, turns-turnsBase, sent))
		}
		time.Sleep(100 * time.Millisecond)
	}

	// CI-scale tripwires: zero loss (asserted above), full drain within 30s
	// of last accept, ingest ≥ 200 ev/s even on a shared runner.
	pass := drain <= 30*time.Second && rate >= 200
	return Probe{
		Name:     name,
		Budget:   "zero loss; drain ≤ 30s after last accept; ingest ≥ 200 ev/s (CI tripwire)",
		Observed: fmt.Sprintf("%d events @ %.0f ev/s ingest, both read models drained %.1fs after last accept", sent, rate, drain.Seconds()),
		Value:    drain.Seconds(),
		Limit:    30,
		Pass:     pass,
		Detail:   fmt.Sprintf("ingest %.2fs, chain and conversation_turns verified at +%d each", ingest.Seconds(), sent),
	}
}

// captureCounts reads the two read-model positions: newest chain seq and the
// session's stored turn count.
func captureCounts(ctx context.Context, w *strazad, sessionID string, events int) (int64, int, error) {
	seq, _, err := w.st.Audit().LastHash(ctx)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return 0, 0, err
	}
	turns, err := w.st.Conversations().ListBySession(ctx, sessionID, events+100)
	if err != nil {
		return 0, 0, err
	}
	return seq, len(turns), nil
}
