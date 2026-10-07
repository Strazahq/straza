package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// scimTranscript mirrors the format documented in spec/scim-profile §7.
type scimTranscript struct {
	Name  string     `json:"name"`
	Steps []scimStep `json:"steps"`
}

type scimStep struct {
	Note    string `json:"note"`
	Request struct {
		Method  string            `json:"method"`
		Path    string            `json:"path"`
		Body    json.RawMessage   `json:"body"`
		NoAuth  bool              `json:"noAuth"`
		Headers map[string]string `json:"headers"`
	} `json:"request"`
	Expect struct {
		Status int `json:"status"`
		Subset any `json:"subset"`
	} `json:"expect"`
	Capture map[string]string `json:"capture"`
}

// TestSCIMConformance replays every transcript in spec/conformance/scim
// against a live strazad: the transcripts ARE the contract.
// The pre-created role "scim-dev" is the exported role the group
// transcripts anchor on (revision 12: roles render as wire-groups; the
// IdM imports and assigns, never creates).
func TestSCIMConformance(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminBearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	scimToken := mintProvisioningToken(t, base, adminBearer)
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/roles", adminBearer,
		map[string]any{"name": "scim-dev", "description": "Developer access (conformance)", "kind": "business"}, nil); code != http.StatusCreated {
		t.Fatalf("seed role scim-dev = %d", code)
	}
	// revision 13 pin: an approver-kind role renders on the wire like any
	// access-plane role (group-enrichment transcript).
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/roles", adminBearer,
		map[string]any{"name": "scim-approvers", "description": "Approval deciders (conformance)", "kind": "approver"}, nil); code != http.StatusCreated {
		t.Fatalf("seed role scim-approvers = %d", code)
	}

	// revision 18 pin: a server's minted admin role renders with the server
	// it administers (group-enrichment transcript). The store mints it.
	if _, err := app.store.Apps().Create(t.Context(), store.App{Name: "scim-tools", RuntimeKind: "remote", Status: "stopped"}); err != nil {
		t.Fatalf("seed server scim-tools: %v", err)
	}
	// revision 19 pin: a server-owned role renders the server that owns it
	// (group-enrichment and groups-roles transcripts).
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/roles", adminBearer,
		map[string]any{"name": "scim-tools-readers", "description": "Readers of scim-tools (conformance)", "server": "scim-tools", "tools": []string{"echo"}}, nil); code != http.StatusCreated {
		t.Fatalf("seed role scim-tools-readers = %d", code)
	}

	dir := filepath.Join("..", "..", "spec", "conformance", "scim")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 5 {
		t.Fatalf("expected a transcript corpus, found %d files", len(entries))
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var tr scimTranscript
		if err := json.Unmarshal(raw, &tr); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		t.Run(tr.Name, func(t *testing.T) {
			runTranscript(t, base, scimToken, tr)
		})
	}
}

func runTranscript(t *testing.T, base, token string, tr scimTranscript) {
	t.Helper()
	vars := map[string]string{}
	for i, step := range tr.Steps {
		label := fmt.Sprintf("step %d (%s)", i+1, step.Note)

		path := substVars(step.Request.Path, vars)
		// Transcripts keep queries human-readable (spaces, quotes); encode
		// them for the wire.
		if pathPart, queryPart, ok := strings.Cut(path, "?"); ok {
			q, err := url.ParseQuery(queryPart)
			if err != nil {
				t.Fatalf("%s: query: %v", label, err)
			}
			path = pathPart + "?" + q.Encode()
		}
		var body io.Reader
		if len(step.Request.Body) > 0 {
			body = bytes.NewReader([]byte(substVars(string(step.Request.Body), vars)))
		}
		req, err := http.NewRequest(step.Request.Method, base+path, body)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		req.Header.Set("Content-Type", "application/scim+json")
		if !step.Request.NoAuth {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		for k, v := range step.Request.Headers {
			req.Header.Set(k, substVars(v, vars))
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode != step.Expect.Status {
			t.Fatalf("%s: status = %d, want %d\nbody: %s", label, resp.StatusCode, step.Expect.Status, respBody)
		}
		var decoded any
		if len(respBody) > 0 {
			_ = json.Unmarshal(respBody, &decoded)
		}
		if step.Expect.Subset != nil {
			if err := subsetMatch(substAny(step.Expect.Subset, vars), decoded); err != nil {
				t.Fatalf("%s: %v\nbody: %s", label, err, respBody)
			}
		}
		if len(step.Capture) > 0 {
			for name, field := range step.Capture {
				v, _ := captureField(decoded, field).(string)
				if v == "" {
					t.Fatalf("%s: capture %s: field %q missing\nbody: %s", label, name, field, respBody)
				}
				vars[name] = v
			}
		}
	}
}

func substVars(s string, vars map[string]string) string {
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s
}

func substAny(v any, vars map[string]string) any {
	switch t := v.(type) {
	case string:
		return substVars(t, vars)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = substAny(e, vars)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = substAny(e, vars)
		}
		return out
	default:
		return v
	}
}

// subsetMatch: objects need every expected key to match; arrays need every
// expected element to match SOME actual element (spec/scim-profile §7).
func subsetMatch(expected, actual any) error {
	switch exp := expected.(type) {
	case map[string]any:
		act, ok := actual.(map[string]any)
		if !ok {
			return fmt.Errorf("expected object, got %T", actual)
		}
		for k, v := range exp {
			av, ok := act[k]
			if !ok {
				return fmt.Errorf("missing key %q", k)
			}
			if err := subsetMatch(v, av); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
		}
		return nil
	case []any:
		act, ok := actual.([]any)
		if !ok {
			return fmt.Errorf("expected array, got %T", actual)
		}
		for _, want := range exp {
			found := false
			for _, have := range act {
				if subsetMatch(want, have) == nil {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("no array element matches %v", want)
			}
		}
		return nil
	default:
		if expected != actual {
			return fmt.Errorf("value = %v, want %v", actual, expected)
		}
		return nil
	}
}

// TestSCIMKillSwitch pins the kill switch beyond the transcripts: `active:
// false` revokes live sessions (denylist + session rows) and emits
// straza.revocation.user; re-activation lifts only the user-level entry.
func TestSCIMKillSwitch(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminBearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	scimToken := mintProvisioningToken(t, base, adminBearer)
	ctx := context.Background()

	// Provision via SCIM, give the user a password so the built-in issuer
	// can log them in (hermetic stand-in for the external IdP).
	uid := scimCreateUser(t, base, scimToken,
		`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"scim.victim","externalId":"idm-666","active":true}`)
	if u, err := app.store.Users().GetByID(ctx, uid); err != nil {
		t.Fatal(err)
	} else if u.Origin != store.OriginSCIM {
		t.Errorf("origin = %q, want scim", u.Origin)
	}
	setUserPassword(t, app, uid, "hunter2!")

	// The user works: checkin succeeds, /v1/decide answers.
	tok := sessionToken(t, base, "scim.victim")
	if code, _, _ := mcpCall(t, base, tok, "tools/list", nil); code != http.StatusOK {
		t.Fatalf("pre-revocation tools/list = %d", code)
	}

	// Deactivate in the "IdM" → SCIM PATCH active:false.
	scimPatch(t, base, scimToken, uid,
		`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":false}]}`)

	// The very next gateway call is denied (in-process propagation is
	// immediate; TestMultiPodHA pins the cross-pod path).
	if code, _, _ := mcpCall(t, base, tok, "tools/list", nil); code != http.StatusForbidden {
		t.Errorf("post-revocation tools/list = %d, want 403", code)
	}

	// Sessions are revoked in the store and the event is emitted.
	sessions, err := app.store.Sessions().ListByUser(ctx, uid)
	if err != nil || len(sessions) == 0 {
		t.Fatalf("sessions = %v err=%v", sessions, err)
	}
	for _, s := range sessions {
		if s.Status != store.SessionRevoked {
			t.Errorf("session %s status = %s, want revoked", s.ID, s.Status)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		events, err := app.store.Outbox().ListUnpublished(ctx, 1000)
		found := false
		if err == nil {
			for _, e := range events {
				if e.Subject == "straza.revocation.user" && strings.Contains(e.CE, uid) {
					found = true
				}
			}
		}
		if found {
			break
		}
		// The relay may already have published it; check the audit trail too.
		recs, _ := app.store.Audit().List(ctx, 0, 1000)
		for _, rec := range recs {
			if strings.Contains(rec.CE, "straza.revocation.user") && strings.Contains(rec.CE, uid) {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no straza.revocation.user event observed")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Re-activation lifts the user denylist entry; old sessions stay dead.
	scimPatch(t, base, scimToken, uid,
		`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":true}]}`)
	if code, _, _ := mcpCall(t, base, tok, "tools/list", nil); code == http.StatusOK {
		t.Error("old session usable after re-activation; sessions must stay revoked")
	}
	tok2 := sessionToken(t, base, "scim.victim")
	if code, _, _ := mcpCall(t, base, tok2, "tools/list", nil); code != http.StatusOK {
		t.Errorf("fresh session after re-activation = %d, want 200", code)
	}
}

// captureField walks a dotted capture path ("Resources.0.id") through maps
// and integer-indexed arrays; a bare field name keeps the historical
// top-level behavior (spec/scim-profile §7).
func captureField(v any, path string) any {
	for _, part := range strings.Split(path, ".") {
		switch cur := v.(type) {
		case map[string]any:
			v = cur[part]
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(cur) {
				return nil
			}
			v = cur[i]
		default:
			return nil
		}
	}
	return v
}

// mintProvisioningToken mints the one credential the SCIM plane takes, an
// admin API token carrying scim:read and scim:write (write does not imply
// read, as on the admin plane), through the real admin endpoint.
func mintProvisioningToken(t *testing.T, base, adminBearer string) string {
	t.Helper()
	var out struct {
		Token string `json:"token"`
	}
	code := adminReq(t, "POST", base+"/v1/admin/api-tokens", adminBearer, map[string]string{"name": "conformance", "scope": "scim:read,scim:write"}, &out)
	if code != http.StatusCreated || out.Token == "" {
		t.Fatalf("api-token create = %d %+v", code, out)
	}
	return out.Token
}
