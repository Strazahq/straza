package server

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/config"
)

// unknownFieldNamed is the part of an unknown field's 400 that names the
// field zz_unknown.
const unknownFieldNamed = `a field named "zz_unknown"`

// TestAdminRoutesRefuseUnknownFields pins that each admin route that
// decodes with decodeAdmin answers 400 naming a field an otherwise valid
// body carries and the route does not take. A misspelled token field mints
// no token, a refused activate leaves the set off, a missing body still
// turns it on, and a draft item read back from a draft is accepted.
func TestAdminRoutesRefuseUnknownFields(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil, func(c *config.Config) {
		c.Sinks = []config.Sink{{Name: "archive", Type: config.SinkFile, Path: filepath.Join(t.TempDir(), "events.jsonl")}}
	})
	ctx := context.Background()
	erin, err := f.app.store.Users().GetByUsername(ctx, "erin")
	if err != nil {
		t.Fatal(err)
	}
	bot, err := f.app.store.Users().GetByUsername(ctx, "bot")
	if err != nil {
		t.Fatal(err)
	}
	packs, err := f.app.store.Packs().List(ctx)
	if err != nil || len(packs) == 0 {
		t.Fatalf("packs = %v, %v", packs, err)
	}
	reader, reviewers := mustRole(t, f.app, "reader"), mustRole(t, f.app, "draft-reviewers")
	ids := strings.NewReplacer("{reader}", reader.ID, "{reviewers}", reviewers.ID, "{erin}", erin.ID, "{bot}", bot.ID,
		"{pack}", packs[0].ID, "{draft}", f.create(t, f.root, draftRole("zz-helpers", "Helpers.")).Draft.ID)
	docs := []string{draftRole("zz-helpers", "Helpers, again.")}
	key := base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))

	for _, tc := range []struct {
		method, path string
		body         map[string]any
	}{
		{http.MethodPost, "/v1/admin/apps/github/secrets", map[string]any{"value": "s3cret"}},
		{http.MethodPost, "/v1/admin/apps/github/objects/credential", map[string]any{"value": "s3cret"}},
		{http.MethodPost, "/v1/admin/apps/github/bindings", map[string]any{"role": "reader"}},
		{http.MethodPost, "/v1/admin/api-tokens", map[string]any{"name": "zz-token", "scope": "full"}},
		{http.MethodPost, "/v1/admin/policies/simulate", map[string]any{
			"event":   map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "ls"},
			"subject": map[string]any{"roles": []string{"dev"}},
		}},
		{http.MethodPost, "/v1/admin/policies/standalone-starter/activate", map[string]any{"status": "active"}},
		{http.MethodPost, "/v1/admin/sinks/archive/replay", map[string]any{"limit": 10}},
		{http.MethodPost, "/v1/admin/approvals/zz/approve", map[string]any{"reason": "Looks right."}},
		{http.MethodPost, "/v1/admin/approvals/zz/deny", map[string]any{"reason": "Not now."}},
		{http.MethodPost, "/v1/admin/roles", map[string]any{"name": "zz-role"}},
		{http.MethodPatch, "/v1/admin/roles/{reader}", map[string]any{"description": "Readers."}},
		{http.MethodPost, "/v1/admin/roles/{reviewers}/implications", map[string]any{"implies_role_id": reader.ID}},
		{http.MethodPost, "/v1/admin/assignments", map[string]any{"subject_kind": "user", "subject_id": erin.ID, "role_id": reader.ID}},
		{http.MethodPost, "/v1/admin/users", map[string]any{"username": "zed"}},
		{http.MethodPatch, "/v1/admin/users/{erin}", map[string]any{"display": "Erin"}},
		{http.MethodPost, "/v1/admin/users/{erin}/lock", map[string]any{"reason": "A test."}},
		{http.MethodPut, "/v1/admin/users/{bot}/nhi-key", map[string]any{"public_key": key}},
		{http.MethodPost, "/v1/admin/attestation-hashes", map[string]any{"artifact": "hooks.claude-code", "harness": "claude-code", "hash": "sha256:abcdef"}},
		{http.MethodPost, "/v1/admin/sessions/revoke", map[string]any{"user": "erin"}},
		{http.MethodPost, "/v1/admin/packs", map[string]any{"name": "zz-pack", "version": "1", "content": "Use gofmt."}},
		{http.MethodPost, "/v1/admin/packs/{pack}/bindings", map[string]any{"role_id": reader.ID}},
		{http.MethodPost, "/v1/admin/drafts", map[string]any{"documents": docs}},
		{http.MethodPost, "/v1/admin/drafts/check", map[string]any{"documents": docs}},
		{http.MethodPut, "/v1/admin/drafts/{draft}", map[string]any{"revision": 1, "documents": docs}},
		{http.MethodPost, "/v1/admin/drafts/{draft}/discard", map[string]any{"revision": 1, "reason": "Not needed."}},
		{http.MethodPost, "/v1/admin/drafts/{draft}/revert", map[string]any{"note": "Undo it."}},
		{http.MethodPost, "/v1/admin/drafts/{draft}/publish", map[string]any{"revision": 1}},
		{http.MethodPost, "/v1/admin/drafts/{draft}/rebase", map[string]any{"revision": 1}},
		{http.MethodPost, "/v1/admin/drafts/{draft}/contact", map[string]any{"object": "App/github"}},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			tc.body["zz_unknown"] = 1
			if code, a := f.call(t, tc.method, ids.Replace(tc.path), f.root, tc.body); code != http.StatusBadRequest || !strings.Contains(a.Error, unknownFieldNamed) {
				t.Errorf("answer = %d %q, want 400 naming zz_unknown", code, a.Error)
			}
		})
	}

	t.Run("a misspelled token field mints nothing", func(t *testing.T) {
		code, out, _ := adminBytes(t, http.MethodPost, f.base+"/v1/admin/api-tokens", f.root, "application/json",
			[]byte(`{"name":"t","scope":"full","expiresIn":3600}`))
		var got struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(out, &got)
		if code != http.StatusBadRequest || !strings.Contains(got.Error, `a field named "expiresIn"`) {
			t.Errorf("mint = %d %q, want 400 naming expiresIn", code, got.Error)
		}
		var tokens []apiTokenMeta
		if code := adminReq(t, http.MethodGet, f.base+"/v1/admin/api-tokens", f.root, nil, &tokens); code != http.StatusOK {
			t.Fatalf("list = %d", code)
		}
		for _, m := range tokens {
			if m.Name == "t" {
				t.Errorf("the refused mint stored the token %+v", m)
			}
		}
	})

	t.Run("activate turns the set on only without a body", func(t *testing.T) {
		if code, out, _ := adminBytes(t, http.MethodPut, f.base+"/v1/admin/policies", f.root, "application/yaml", []byte(rmPolicy)); code != http.StatusCreated {
			t.Fatalf("apply = %d %s", code, out)
		}
		for _, tc := range []struct {
			name, body string
			code       int
			says       string
			status     string
		}{
			{"an unknown field", `{"status":"draft","zz_unknown":1}`, http.StatusBadRequest, unknownFieldNamed, "draft"},
			{"a malformed body", `{"status":`, http.StatusBadRequest,
				`The request body is not valid JSON: unexpected EOF. Send {"status": "active"} or {"status": "draft"}, or no body to turn the set on.`, "draft"},
			{"no body", "", http.StatusOK, "", "active"},
		} {
			code, out, _ := adminBytes(t, http.MethodPost, f.base+"/v1/admin/policies/block-rm/activate", f.root, "application/json", []byte(tc.body))
			var got struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(out, &got)
			if code != tc.code || !strings.Contains(got.Error, tc.says) {
				t.Errorf("%s: activate = %d %q, want %d %q", tc.name, code, got.Error, tc.code, tc.says)
			}
			row, err := f.app.store.Policies().GetByName(ctx, "block-rm")
			if err != nil {
				t.Fatal(err)
			}
			if row.Status != tc.status {
				t.Errorf("%s: the set is %s, want %s", tc.name, row.Status, tc.status)
			}
		}
	})

	t.Run("a draft item read back is accepted", func(t *testing.T) {
		doc := draftRole("zz-read-back", "Read back.")
		for _, tc := range []struct {
			name string
			item map[string]any
			code int
			says string
		}{
			{"the read side's fields", map[string]any{"kind": "Role", "name": "zz-read-back", "op": "put", "doc": doc,
				"base": "fp-from-the-client", "existed": true, "withheld": "held"}, http.StatusCreated, ""},
			{"an unknown field", map[string]any{"kind": "Role", "name": "zz-read-back", "op": "put", "doc": doc,
				"zz_unknown": 1}, http.StatusBadRequest, unknownFieldNamed},
		} {
			before := f.drafts(t)
			code, a := f.call(t, http.MethodPost, "/v1/admin/drafts", f.root, map[string]any{"items": []map[string]any{tc.item}})
			if code != tc.code || !strings.Contains(a.Error, tc.says) {
				t.Errorf("%s: create = %d %q %v, want %d %q", tc.name, code, a.Error, a.Findings, tc.code, tc.says)
			}
			if after := f.drafts(t); tc.code != http.StatusCreated && after != before {
				t.Errorf("%s: the store holds %d drafts after the refusal, want %d", tc.name, after, before)
			}
		}
	})
}
