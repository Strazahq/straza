package main

// Fleet-rig lane helpers: the per-lane HTTP drivers and cohort utilities
// probe_fleet.go composes. Split out to keep the probe file readable.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// fleetApprover creates one user holding fleet-approvers (a distinct human
// identity, since self-approval is server-refused) and mints its login token.
// The role is approver-kind (spec/policyset revision 15): only approver
// roles may sit in approve.roles, and the rig's approve-gate names this one.
func fleetApprover(ctx context.Context, w *strazad) (string, error) {
	u, err := w.st.Users().Create(ctx, store.User{Username: "fleet-approver", Email: "fleet-approver@load.test"})
	if err != nil {
		return "", err
	}
	role, err := w.st.Roles().GetByName(ctx, "fleet-approvers")
	if err != nil {
		role, err = w.st.Roles().Create(ctx, store.Role{Name: "fleet-approvers", Kind: store.RoleKindApprover})
		if err != nil {
			return "", err
		}
	}
	if _, err := w.st.Roles().Assign(ctx, store.RoleAssignment{
		SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: role.ID,
	}); err != nil {
		return "", err
	}
	return w.tokens.MintIDToken(u.ID, "strazactl", time.Hour, u.Username, u.Email)
}

// waitCatalog polls tools/list until toolName appears (the app runtime is up
// and bound) or the deadline passes.
func waitCatalog(w *strazad, sessionTok, toolName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	for {
		req, _ := http.NewRequest(http.MethodPost, w.base+"/mcp", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+sessionTok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			raw, _ := readAll(resp)
			if strings.Contains(raw, toolName) {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("tool %s not in catalog within %s", toolName, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// postTurn sends one prompt+reply pair (~2 KiB each) to /v1/audit/batch and
// returns the accepted count.
var turnContent = strings.Repeat("x", 2<<10)

func postTurn(ctx context.Context, w *strazad, client *http.Client, sessionTok string, member, seq int) (int, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	mk := func(kind string, i int) string {
		return fmt.Sprintf(`{"specversion":"1.0","id":"fleet-%05d-%06d-%d","type":"straza.audit.%s","source":"straza","time":%q,"data":{"content":%q,"mode":"verbatim","truncated":false,"contentHash":"sha256:fleet"}}`,
			member, seq, i, kind, now, turnContent)
	}
	body := fmt.Sprintf(`{"events":[%s,%s]}`, mk("prompt", 0), mk("reply", 1))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.base+"/v1/audit/batch", strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+sessionTok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	var out struct {
		Accepted int `json:"accepted"`
	}
	err = json.NewDecoder(resp.Body).Decode(&out)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("audit batch: HTTP %d", resp.StatusCode)
	}
	if err != nil {
		return 0, err
	}
	return out.Accepted, nil
}

// callEcho drives one plain-allow governed tool call.
func callEcho(w *strazad, client *http.Client, sessionTok string) error {
	req, _ := http.NewRequest(http.MethodPost, w.base+"/mcp", strings.NewReader(echoCallBody))
	req.Header.Set("Authorization", "Bearer "+sessionTok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	raw, _ := readAll(resp)
	if resp.StatusCode != http.StatusOK || strings.Contains(raw, `"error"`) {
		return fmt.Errorf("echo: HTTP %d %s", resp.StatusCode, raw)
	}
	return nil
}

// callGate drives the approval-gated tool. The gateway BLOCKS the call in its
// Await lane until the decision lands, so this needs its own long-timeout
// client; success = the upstream result after an approve.
var gateClient = &http.Client{Timeout: 90 * time.Second}

func callGate(w *strazad, sessionTok string) error {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"loadtest__gate","arguments":{"text":"ticket"}}}`
	req, _ := http.NewRequest(http.MethodPost, w.base+"/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+sessionTok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := gateClient.Do(req)
	if err != nil {
		return err
	}
	raw, _ := readAll(resp)
	if resp.StatusCode != http.StatusOK || strings.Contains(raw, `"error"`) {
		return fmt.Errorf("gate: HTTP %d %s", resp.StatusCode, raw)
	}
	return nil
}

// awaitPending polls the admin approvals list until an id outside seen shows
// up; returns it and the time it became visible.
func awaitPending(w *strazad, client *http.Client, adminTok string, seen map[string]bool, timeout time.Duration) (string, time.Time, error) {
	deadline := time.Now().Add(timeout)
	for {
		req, _ := http.NewRequest(http.MethodGet, w.base+"/v1/admin/approvals?state=pending", nil)
		req.Header.Set("Authorization", "Bearer "+adminTok)
		resp, err := client.Do(req)
		if err == nil {
			var out struct {
				Approvals []struct {
					ID string `json:"id"`
				} `json:"approvals"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&out)
			_ = resp.Body.Close()
			for _, a := range out.Approvals {
				if !seen[a.ID] {
					seen[a.ID] = true
					return a.ID, time.Now(), nil
				}
			}
		}
		if time.Now().After(deadline) {
			return "", time.Time{}, fmt.Errorf("no pending approval visible within %s", timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func decideApproval(w *strazad, client *http.Client, approverTok, id string) error {
	req, _ := http.NewRequest(http.MethodPost, w.base+"/v1/admin/approvals/"+id+"/approve", nil)
	req.Header.Set("Authorization", "Bearer "+approverTok)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	raw, _ := readAll(resp)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("approve %s: HTTP %d %s", id, resp.StatusCode, raw)
	}
	return nil
}

// bindListChanged writes one binding row; any binding CRUD invalidates the
// catalog and broadcasts list_changed to every standing /mcp stream. The
// row lands on a throwaway role of the loadtest app (approver roles cannot
// bind tools, revision 15, and a role of no server gains no row), stored
// fresh for each call with no row, so every call writes a row; nobody holds
// it, the broadcast is the point.
func bindListChanged(ctx context.Context, w *strazad, client *http.Client, adminTok string) error {
	app, err := w.st.Apps().GetByName(ctx, "loadtest")
	if err != nil {
		return err
	}
	name := fmt.Sprintf("loadtest-listchanged-%d", time.Now().UnixNano())
	if _, err := w.st.Roles().Create(ctx, store.Role{Name: name, Kind: store.RoleKindApplication, OwnerAppID: app.ID}); err != nil {
		return err
	}
	bind, _ := json.Marshal(map[string]any{"role": name, "tools": []string{"*"}})
	req, _ := http.NewRequest(http.MethodPost, w.base+"/v1/admin/apps/loadtest/bindings", bytes.NewReader(bind))
	req.Header.Set("Authorization", "Bearer "+adminTok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	raw, _ := readAll(resp)
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("bind: HTTP %d %s", resp.StatusCode, raw)
	}
	return nil
}

type fleetCaptureSample struct {
	sid  string
	sent int64
}

// fleetCaptureSamples picks up to n captured members that were never killed,
// spread across the cohort, with their personal sent counts.
func fleetCaptureSamples(fleet []*fleetMember, perMemberSent []int64, o fleetOpts, n int) []fleetCaptureSample {
	var out []fleetCaptureSample
	for _, m := range fleet {
		if len(out) >= n {
			break
		}
		sent := atomic.LoadInt64(&perMemberSent[m.idx])
		if inCohort(m.idx, len(fleet), o.capturePct) && sent > 0 && m.idx < len(fleet)-o.kills {
			out = append(out, fleetCaptureSample{sid: m.sid, sent: sent})
		}
	}
	return out
}

// inCohort puts the first pct% of the fleet in a cohort, exact at any fleet
// size (idx%100 breaks below 100 agents: every index passes).
func inCohort(idx, agents, pct int) bool {
	return idx*100 < agents*pct
}

func sleepUntil(ctx context.Context, t time.Time) {
	d := time.Until(t)
	if d <= 0 {
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func fdCount() int {
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0
	}
	return len(ents)
}

func readAll(resp *http.Response) (string, error) {
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return string(raw), err
}
