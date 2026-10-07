package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

const rmPolicy = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: block-rm }
spec:
  match: { roles: [dev] }
  rules:
    - id: no-rm-rf
      tools: [shell.exec]
      command: { denyPatterns: ["rm -rf *"] }
      effect: deny
      reason: "Destructive delete blocked"
`

func adminBytes(t *testing.T, method, urlStr, bearer, ct string, body []byte) (int, []byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, urlStr, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, buf, resp.Header
}

// TestUsersKind pins the IGA-facing identity-kind surface: an agentic-SCIM
// user (stored attrs.kind=nhi, spec/scim-profile §5) lists as kind "nhi",
// everyone else as "human"; origin keeps answering "who created it", kind
// answers "what it is", so pull connectors can archetype non-human
// identities without an out-of-band convention.
func TestUsersKind(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)

	if _, err := app.store.Users().Create(context.Background(), store.User{
		Username: "atlas-nhi", Email: "atlas@x.io", Attrs: `{"kind":"nhi"}`,
	}); err != nil {
		t.Fatal(err)
	}

	code, body, _ := adminBytes(t, "GET", base+"/v1/admin/users", tok, "", nil)
	if code != http.StatusOK {
		t.Fatalf("list users = %d", code)
	}
	var users []map[string]any
	if err := jsonUnmarshal(body, &users); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, u := range users {
		kinds[u["username"].(string)], _ = u["kind"].(string)
	}
	if kinds["atlas-nhi"] != "nhi" {
		t.Errorf("agentic user kind = %q, want nhi", kinds["atlas-nhi"])
	}
	if kinds["kim"] != "human" {
		t.Errorf("plain user kind = %q, want human", kinds["kim"])
	}
}

// TestPolicyDelete pins the Policy Builder's delete lane: drafts delete
// (204, gone from the list), ACTIVE sets refuse (409, since deletion must never
// change enforcement as a side effect; deactivate recompiles deliberately),
// unknown names 404, and the list payload carries updated_at.
// TestPolicyApplyMultiDocRejected pins that a multi-document upload is
// refused outright: Parse-the-first-doc semantics would store the WHOLE
// file under doc 1's name, so the remaining documents would look applied
// and never enforce. The client (strazactl) splits multi-doc files and
// applies each document separately.
func TestPolicyApplyMultiDocRejected(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)

	multi := rmPolicy + "---\n" + `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: second-set }
spec:
  rules:
    - id: allow-read
      tools: [file.read]
      effect: allow
`
	code, body, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", tok, "application/yaml", []byte(multi))
	if code != http.StatusBadRequest {
		t.Fatalf("multi-doc apply = %d, want 400", code)
	}
	if !strings.Contains(string(body), "2 PolicySet documents") {
		t.Errorf("rejection must name the document count: %s", body)
	}
	// Neither document may have been stored.
	code, list, _ := adminBytes(t, "GET", base+"/v1/admin/policies", tok, "", nil)
	if code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	for _, name := range []string{"block-rm", "second-set"} {
		if strings.Contains(string(list), name) {
			t.Errorf("rejected multi-doc upload still stored %q", name)
		}
	}
}

func TestPolicyDelete(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)

	code, _, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", tok, "application/yaml", []byte(rmPolicy))
	if code != http.StatusCreated {
		t.Fatalf("apply = %d", code)
	}

	code, body, _ := adminBytes(t, "GET", base+"/v1/admin/policies", tok, "", nil)
	if code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	var env struct {
		Items []map[string]any `json:"items"`
	}
	if err := jsonUnmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range env.Items {
		if s["name"] == "block-rm" {
			found = true
			if ua, _ := s["updated_at"].(string); ua == "" {
				t.Errorf("list row carries no updated_at: %v", s)
			}
		}
	}
	if !found {
		t.Fatal("block-rm not listed after apply")
	}

	// Active sets refuse deletion.
	code, _, _ = adminBytes(t, "POST", base+"/v1/admin/policies/block-rm/activate", tok, "application/json", []byte(`{"status":"active"}`))
	if code != http.StatusOK {
		t.Fatalf("activate = %d", code)
	}
	code, _, _ = adminBytes(t, "DELETE", base+"/v1/admin/policies/block-rm", tok, "", nil)
	if code != http.StatusConflict {
		t.Fatalf("delete active = %d, want 409", code)
	}

	// Deactivated drafts delete and disappear.
	code, body, _ = adminBytes(t, "POST", base+"/v1/admin/policies/block-rm/activate", tok, "application/json", []byte(`{"status":"draft"}`))
	if code != http.StatusOK {
		t.Fatalf("deactivate = %d: %s", code, body)
	}
	code, _, _ = adminBytes(t, "DELETE", base+"/v1/admin/policies/block-rm", tok, "", nil)
	if code != http.StatusNoContent {
		t.Fatalf("delete draft = %d, want 204", code)
	}
	code, body, _ = adminBytes(t, "GET", base+"/v1/admin/policies", tok, "", nil)
	if code != http.StatusOK {
		t.Fatalf("list after delete = %d", code)
	}
	if bytes.Contains(body, []byte("block-rm")) {
		t.Errorf("block-rm still listed after delete: %s", body)
	}

	code, _, _ = adminBytes(t, "DELETE", base+"/v1/admin/policies/block-rm", tok, "", nil)
	if code != http.StatusNotFound {
		t.Fatalf("delete unknown = %d, want 404", code)
	}
}

// TestPolicyApplyActivateDistribute drives apply → activate →
// snapshot recompiles → GET /v1/snapshot serves signed bytes → the client
// verifies them against the published keys → tampering is rejected → the
// engine enforces the policy.
func TestPolicyApplyActivateDistribute(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")

	// Boot snapshot exists (empty policy set) and is served.
	code, body, hdr := adminBytes(t, "GET", base+"/v1/snapshot", "", "", nil)
	if code != http.StatusOK || hdr.Get("ETag") == "" {
		t.Fatalf("initial snapshot = %d, etag %q", code, hdr.Get("ETag"))
	}
	bootETag := hdr.Get("ETag")
	_ = body

	// If-None-Match short-circuits to 304.
	req, _ := http.NewRequest("GET", base+"/v1/snapshot", nil)
	req.Header.Set("If-None-Match", bootETag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Errorf("If-None-Match = %d, want 304", resp.StatusCode)
	}

	// Apply the policy (YAML upload) and activate it.
	if code, b, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", idToken, "application/yaml", []byte(rmPolicy)); code != http.StatusCreated {
		t.Fatalf("apply = %d %s", code, b)
	}
	var activated struct {
		Snapshot string `json:"snapshot"`
	}
	if code := adminReq(t, "POST", base+"/v1/admin/policies/block-rm/activate", idToken, map[string]string{"status": "active"}, &activated); code != http.StatusOK {
		t.Fatalf("activate = %d", code)
	}
	if activated.Snapshot == "" || activated.Snapshot == bootETag {
		t.Fatalf("snapshot did not change on activate: %q", activated.Snapshot)
	}

	// The new snapshot is served with the new id as ETag (atomic swap).
	code, signed, hdr := adminBytes(t, "GET", base+"/v1/snapshot", "", "", nil)
	if code != http.StatusOK {
		t.Fatalf("snapshot fetch = %d", code)
	}
	newID := hdr.Get("X-Straza-Snapshot-Id")
	if newID != activated.Snapshot {
		t.Fatalf("served snapshot %q != activated %q", newID, activated.Snapshot)
	}

	// Fetch the published verification keys and open the snapshot as a
	// client would: signature must verify and the engine must enforce.
	keys := fetchSnapshotKeys(t, base)
	eng, snap, err := policy.OpenSnapshot(signed, newID, keys)
	if err != nil {
		t.Fatalf("client verify: %v", err)
	}
	if snap.MaxAgeSecs != int64((15 * 60)) { // standalone grace default 15m
		t.Errorf("snapshot maxAge = %d", snap.MaxAgeSecs)
	}
	d := eng.Evaluate(
		policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "rm -rf /"},
		policy.Subject{User: "kim", Roles: []string{"dev"}},
	)
	if d.Effect != policy.EffectDeny {
		t.Fatalf("distributed policy not enforced: %+v", d)
	}

	// Tampering with the served bytes is rejected against the same keys.
	tampered := make([]byte, len(signed))
	copy(tampered, signed)
	tampered[len(tampered)/2] ^= 0xFF
	if _, _, err := policy.OpenSnapshot(tampered, "", keys); err == nil {
		t.Fatal("tampered distributed snapshot accepted")
	}

	// policy.updated events landed in the outbox.
	pending, _ := app.store.Outbox().ListUnpublished(context.Background(), 50)
	found := false
	for _, e := range pending {
		if e.Subject == "straza.policy.updated" {
			found = true
		}
	}
	if !found {
		t.Error("no straza.policy.updated event emitted")
	}
}

func fetchSnapshotKeys(t *testing.T, base string) policy.KeyLookup {
	t.Helper()
	code, body, _ := adminBytes(t, "GET", base+"/.well-known/straza/snapshot-keys.json", "", "", nil)
	if code != http.StatusOK {
		t.Fatalf("snapshot-keys = %d", code)
	}
	var doc struct {
		Keys []struct {
			KID string `json:"kid"`
			Key string `json:"key"`
		} `json:"keys"`
	}
	if err := jsonUnmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	keys := map[string]ed25519.PublicKey{}
	for _, k := range doc.Keys {
		raw, err := base64.StdEncoding.DecodeString(k.Key)
		if err != nil {
			t.Fatal(err)
		}
		keys[k.KID] = ed25519.PublicKey(raw)
	}
	return func(kid string) (ed25519.PublicKey, bool) {
		pub, ok := keys[kid]
		return pub, ok
	}
}
