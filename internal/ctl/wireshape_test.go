package ctl

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/strazahq/straza/internal/audit"
	"github.com/strazahq/straza/internal/connect"
)

// wireRecorder is a one-route-at-a-time admin API stub: it records the last
// request's wire shape and answers with whatever the current row scripted.
type wireRecorder struct {
	mu      sync.Mutex
	respond string
	method  string
	uri     string // escaped path + raw query
	ct      string
	body    []byte
}

func (rec *wireRecorder) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rec.mu.Lock()
	rec.method, rec.uri, rec.ct, rec.body = r.Method, r.URL.RequestURI(), r.Header.Get("Content-Type"), body
	resp := rec.respond
	rec.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(resp))
}

func (rec *wireRecorder) script(respond string) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.respond, rec.method, rec.uri, rec.ct, rec.body = respond, "", "", "", nil
}

func (rec *wireRecorder) last() (method, uri, ct string, body []byte) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.method, rec.uri, rec.ct, rec.body
}

// TestAdminWrapperWireShapes pins every thin admin wrapper to its wire
// contract (pkg/api/openapi.yaml): verb, escaped path + query, request body,
// and that the canned response decodes into the wrapper's return value.
func TestAdminWrapperWireShapes(t *testing.T) {
	rec := &wireRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(srv.Close)
	c := loggedInClient(t, srv.URL)
	ctx := context.Background()

	tests := []struct {
		name    string
		respond string
		call    func() (decoded bool, err error)
		method  string
		uri     string
		body    map[string]any // exact JSON body; nil = no body on the wire
		rawBody string         // exact non-JSON body (uploads)
		rawCT   string         // Content-Type for rawBody rows
	}{
		{
			name:    "CreateNHI",
			respond: `{"id":"u1","username":"svc-scan","display":"Scanner","status":"active"}`,
			call: func() (bool, error) {
				u, err := c.CreateNHI(ctx, "svc-scan", "Scanner", "service")
				return u.ID == "u1" && u.Username == "svc-scan", err
			},
			method: http.MethodPost, uri: "/v1/admin/users",
			body: map[string]any{"username": "svc-scan", "display": "Scanner", "kind": "nhi", "user_type": "service"},
		},
		{
			name:    "SetNHIKey",
			respond: `{}`,
			call:    func() (bool, error) { return true, c.SetNHIKey(ctx, "u1", "cHViLWtleQ") },
			method:  http.MethodPut, uri: "/v1/admin/users/u1/nhi-key",
			body: map[string]any{"public_key": "cHViLWtleQ"},
		},
		{
			name:    "UnsetNHIKey",
			respond: `{}`,
			call:    func() (bool, error) { return true, c.UnsetNHIKey(ctx, "u1") },
			method:  http.MethodDelete, uri: "/v1/admin/users/u1/nhi-key",
		},
		{
			name:    "SetUserStatus",
			respond: `{"id":"u1","username":"bob","status":"disabled"}`,
			call: func() (bool, error) {
				u, err := c.SetUserStatus(ctx, "u1", "disabled")
				return u.Status == "disabled", err
			},
			method: http.MethodPatch, uri: "/v1/admin/users/u1",
			body: map[string]any{"status": "disabled"},
		},
		{
			name:    "LockUser",
			respond: `{"id":"u1","locked":true,"origin":"external","reason":"SOC hold"}`,
			call:    func() (bool, error) { return true, c.LockUser(ctx, "u1", "SOC hold", "external") },
			method:  http.MethodPost, uri: "/v1/admin/users/u1/lock",
			body: map[string]any{"reason": "SOC hold", "origin": "external"},
		},
		{
			name:    "LockUserDefaultOrigin",
			respond: `{"id":"u1","locked":true,"origin":"admin","reason":"hold"}`,
			call:    func() (bool, error) { return true, c.LockUser(ctx, "u1", "hold", "") },
			method:  http.MethodPost, uri: "/v1/admin/users/u1/lock",
			body: map[string]any{"reason": "hold"},
		},
		{
			name:    "UnlockUser",
			respond: `{"id":"u1","locked":false}`,
			call:    func() (bool, error) { return true, c.UnlockUser(ctx, "u1") },
			method:  http.MethodPost, uri: "/v1/admin/users/u1/unlock",
		},
		{
			name:    "UserDevices",
			respond: `[{"id":"d1","user_id":"u1","name":"kim-laptop","platform":"windows","status":"active"}]`,
			call: func() (bool, error) {
				devs, err := c.UserDevices(ctx, "u1")
				return len(devs) == 1 && devs[0].ID == "d1" && devs[0].Name == "kim-laptop", err
			},
			method: http.MethodGet, uri: "/v1/admin/users/u1/devices",
		},
		{
			// Path-escaping pin on both ids: neither may smuggle path segments.
			name:    "RevokeDevice",
			respond: `{"status":"revoked"}`,
			call:    func() (bool, error) { return true, c.RevokeDevice(ctx, "u one", "d one") },
			method:  http.MethodDelete, uri: "/v1/admin/users/u%20one/devices/d%20one",
		},
		{
			name:    "Audit",
			respond: `[{"seq":7,"ce":"{}","prevHash":"p","hash":"h","username":"bob"}]`,
			call: func() (bool, error) {
				recs, err := c.Audit(ctx, 5, 50, "bob")
				return len(recs) == 1 && recs[0].Seq == 7 && recs[0].Username == "bob", err
			},
			method: http.MethodGet, uri: "/v1/admin/audit?after=5&limit=50&user=bob",
		},
		{
			name:    "AuditRecent",
			respond: `[{"seq":9,"ce":"{}","prevHash":"p","hash":"h","username":"bob"}]`,
			call: func() (bool, error) {
				recs, err := c.AuditRecent(ctx, 50, "bob", "", "")
				return len(recs) == 1 && recs[0].Seq == 9 && recs[0].Username == "bob", err
			},
			method: http.MethodGet, uri: "/v1/admin/audit?order=desc&limit=50&user=bob",
		},
		{
			name:    "AuditRecent by type and text",
			respond: `[{"seq":9,"ce":"{}","prevHash":"p","hash":"h"}]`,
			call: func() (bool, error) {
				recs, err := c.AuditRecent(ctx, 20, "", "straza.audit.mcp", "scout tools")
				return len(recs) == 1, err
			},
			method: http.MethodGet, uri: "/v1/admin/audit?order=desc&limit=20&type=straza.audit.mcp&q=scout+tools",
		},
		{
			name:    "Apps",
			respond: `[{"id":"a1","name":"gh","status":"running","tools":["issues.create"]}]`,
			call: func() (bool, error) {
				apps, err := c.Apps(ctx)
				return len(apps) == 1 && apps[0].Name == "gh" && len(apps[0].Tools) == 1, err
			},
			method: http.MethodGet, uri: "/v1/admin/apps",
		},
		{
			name:    "InstallApp",
			respond: `{"id":"a1","name":"gh","status":"running"}`,
			call: func() (bool, error) {
				app, err := c.InstallApp(ctx, []byte("name: gh\n"))
				return app.ID == "a1", err
			},
			method: http.MethodPost, uri: "/v1/admin/apps",
			rawBody: "name: gh\n", rawCT: "application/yaml",
		},
		{
			name:    "RemoveApp",
			respond: `{}`,
			call:    func() (bool, error) { return true, c.RemoveApp(ctx, "gh") },
			method:  http.MethodDelete, uri: "/v1/admin/apps/gh",
		},
		{
			name:    "Tools",
			respond: `[{"id":"gh:issues.create","app":"gh","app_id":"a1","name":"issues.create","description":"Open an issue"}]`,
			call: func() (bool, error) {
				tools, err := c.Tools(ctx)
				return len(tools) == 1 && tools[0].ID == "gh:issues.create" && tools[0].Description == "Open an issue", err
			},
			method: http.MethodGet, uri: "/v1/admin/tools",
		},
		{
			name:    "RecheckApp",
			respond: `{"id":"a1","name":"gh","status":"running","last_probe_at":"2026-07-17T10:00:00Z","last_healthy_at":"2026-07-17T10:00:00Z"}`,
			call: func() (bool, error) {
				app, err := c.RecheckApp(ctx, "gh")
				return app.Status == "running" && app.LastProbeAt == "2026-07-17T10:00:00Z" && app.LastHealthyAt != "", err
			},
			method: http.MethodPost, uri: "/v1/admin/apps/gh/health",
		},
		{
			name:    "AppLogs",
			respond: `{"lines":["boot","ready"]}`,
			call: func() (bool, error) {
				lines, err := c.AppLogs(ctx, "gh", 25)
				return len(lines) == 2 && lines[1] == "ready", err
			},
			method: http.MethodGet, uri: "/v1/admin/apps/gh/logs?limit=25",
		},
		{
			name:    "SetAppSecret for one role",
			respond: `{"id":"s1","app":"gh","scope":"role","role":"dev","fingerprint":"a1c4"}`,
			call: func() (bool, error) {
				s, err := c.SetAppSecret(ctx, "gh", "dev", "tok-123")
				return s.Scope == "role" && s.Fingerprint == "a1c4", err
			},
			method: http.MethodPost, uri: "/v1/admin/apps/gh/secrets",
			body: map[string]any{"role": "dev", "value": "tok-123"},
		},
		{
			// No role on the wire means the server's own secret.
			name:    "SetAppSecret for the server",
			respond: `{"id":"s2","app":"gh","scope":"app","role":"","fingerprint":"b7e0"}`,
			call: func() (bool, error) {
				s, err := c.SetAppSecret(ctx, "gh", "", "tok-123")
				return s.Scope == "app" && s.Fingerprint == "b7e0", err
			},
			method: http.MethodPost, uri: "/v1/admin/apps/gh/secrets",
			body: map[string]any{"value": "tok-123"},
		},
		{
			name:    "AppSecrets",
			respond: `[{"id":"s2","scope":"app","role":"","kind":"static","fingerprint":"b7e0","set_at":"2026-09-04T07:41:00Z"}]`,
			call: func() (bool, error) {
				rows, err := c.AppSecrets(ctx, "gh")
				return len(rows) == 1 && rows[0].Scope == "app" && rows[0].SetAt == "2026-09-04T07:41:00Z", err
			},
			method: http.MethodGet, uri: "/v1/admin/apps/gh/secrets",
		},
		{
			name:    "RemoveAppSecret for the server",
			respond: `{"id":"s2","status":"deleted"}`,
			call:    func() (bool, error) { return true, c.RemoveAppSecret(ctx, "gh", "") },
			method:  http.MethodDelete, uri: "/v1/admin/apps/gh/secrets",
		},
		{
			name:    "RemoveAppSecret for one role",
			respond: `{"id":"s1","status":"deleted"}`,
			call:    func() (bool, error) { return true, c.RemoveAppSecret(ctx, "gh", "dev ops") },
			method:  http.MethodDelete, uri: "/v1/admin/apps/gh/secrets/dev%20ops",
		},
		{
			name:    "DeletePolicy",
			respond: ``,
			call:    func() (bool, error) { return true, c.DeletePolicy(ctx, "dev guardrails") },
			method:  http.MethodDelete, uri: "/v1/admin/policies/dev%20guardrails",
		},
		{
			name:    "BindAppTools",
			respond: `{"id":"b1","app":"gh","role":"dev","tools":["issues.create"]}`,
			call: func() (bool, error) {
				b, err := c.BindAppTools(ctx, "gh", "dev", []string{"issues.create"})
				return b.ID == "b1" && b.Role == "dev", err
			},
			method: http.MethodPost, uri: "/v1/admin/apps/gh/bindings",
			body: map[string]any{"role": "dev", "tools": []any{"issues.create"}},
		},
		{
			name:    "Bindings",
			respond: `[{"id":"b1","app":"gh","role":"dev","tools":["issues.create"]}]`,
			call: func() (bool, error) {
				bs, err := c.Bindings(ctx)
				return len(bs) == 1 && bs[0].App == "gh", err
			},
			method: http.MethodGet, uri: "/v1/admin/bindings",
		},
		{
			name:    "UnbindTools",
			respond: `{}`,
			call:    func() (bool, error) { return true, c.UnbindTools(ctx, "b1") },
			method:  http.MethodDelete, uri: "/v1/admin/bindings/b1",
		},
		{
			name:    "CreateAPIToken",
			respond: `{"id":"t2","name":"iga","scope":"read","token":"plain-once"}`,
			call: func() (bool, error) {
				tok, err := c.CreateAPIToken(ctx, "iga", "read", 0)
				return tok.ID == "t2" && tok.Token == "plain-once", err
			},
			method: http.MethodPost, uri: "/v1/admin/api-tokens",
			body: map[string]any{"name": "iga", "scope": "read"},
		},
		{
			// Empty scope must stay off the wire so the server default applies.
			name:    "CreateAPIToken default scope",
			respond: `{"id":"t3","name":"iga-ro"}`,
			call: func() (bool, error) {
				tok, err := c.CreateAPIToken(ctx, "iga-ro", "", 0)
				return tok.ID == "t3", err
			},
			method: http.MethodPost, uri: "/v1/admin/api-tokens",
			body: map[string]any{"name": "iga-ro"},
		},
		{
			name:    "APITokens",
			respond: `[{"id":"t2","name":"iga","scope":"read"}]`,
			call: func() (bool, error) {
				toks, err := c.APITokens(ctx)
				return len(toks) == 1 && toks[0].Scope == "read", err
			},
			method: http.MethodGet, uri: "/v1/admin/api-tokens",
		},
		{
			// Path-escaping pin: a raw id must not smuggle path segments.
			name:    "SessionTranscript",
			respond: `{"session_id":"ses one","username":"bob","turns":[{"kind":"prompt","content":"hi"}]}`,
			call: func() (bool, error) {
				tr, err := c.SessionTranscript(ctx, "ses one")
				return tr.Username == "bob" && len(tr.Turns) == 1, err
			},
			method: http.MethodGet, uri: "/v1/admin/sessions/ses%20one/transcript",
		},
		{
			// url.Values.Encode sorts keys; the query order below is canonical.
			name:    "SearchTranscripts",
			respond: `[{"kind":"prompt","content":"AKIA…","username":"bob"}]`,
			call: func() (bool, error) {
				turns, err := c.SearchTranscripts(ctx, "AKIA", "beef", "bob", 5)
				return len(turns) == 1 && turns[0].Username == "bob", err
			},
			method: http.MethodGet, uri: "/v1/admin/transcripts/search?hash=beef&limit=5&q=AKIA&user=bob",
		},
		{
			name:    "AttestationHashes",
			respond: `[{"id":"ah1","artifact":"straza.exe","hash":"ab12"}]`,
			call: func() (bool, error) {
				hs, err := c.AttestationHashes(ctx)
				return len(hs) == 1 && hs[0].Hash == "ab12", err
			},
			method: http.MethodGet, uri: "/v1/admin/attestation-hashes",
		},
		{
			// created_at rides along as the zero timestamp: time.Time is a
			// struct, so encoding/json omitempty never drops it.
			name:    "AddAttestationHash",
			respond: `{"id":"ah1","artifact":"straza.exe","hash":"ab12"}`,
			call: func() (bool, error) {
				h, err := c.AddAttestationHash(ctx, AttestationHash{
					Artifact: "straza.exe", Harness: "claude", Hash: "ab12", Note: "v1",
				})
				return h.ID == "ah1", err
			},
			method: http.MethodPost, uri: "/v1/admin/attestation-hashes",
			body: map[string]any{
				"artifact": "straza.exe", "harness": "claude", "hash": "ab12",
				"note": "v1", "created_at": "0001-01-01T00:00:00Z",
			},
		},
		{
			name:    "RemoveAttestationHash",
			respond: `{}`,
			call:    func() (bool, error) { return true, c.RemoveAttestationHash(ctx, "ah1") },
			method:  http.MethodDelete, uri: "/v1/admin/attestation-hashes/ah1",
		},
		{
			name:    "CreateRole with kind",
			respond: `{"id":"r1","name":"payments","description":"money","kind":"application"}`,
			call: func() (bool, error) {
				r, err := c.CreateRole(ctx, "payments", "money", "application", "", nil)
				return r.ID == "r1" && r.Kind == "application", err
			},
			method: http.MethodPost, uri: "/v1/admin/roles",
			body: map[string]any{"name": "payments", "description": "money", "kind": "application"},
		},
		{
			// Empty kind must stay off the wire so the server default applies.
			name:    "CreateRole default kind",
			respond: `{"id":"r2","name":"finance","description":"money people","kind":"business"}`,
			call: func() (bool, error) {
				r, err := c.CreateRole(ctx, "finance", "money people", "", "", nil)
				return r.ID == "r2" && r.Kind == "business", err
			},
			method: http.MethodPost, uri: "/v1/admin/roles",
			body: map[string]any{"name": "finance", "description": "money people"},
		},
		{
			// Both operands are names: the recorded call is the write that
			// follows the two GET /v1/admin/roles resolutions (the canned
			// response is the role list those read).
			name:    "AddRoleImplication",
			respond: `[{"id":"r1","name":"dev"},{"id":"r2","name":"ops"}]`,
			call:    func() (bool, error) { return true, c.AddRoleImplication(ctx, "dev", "ops") },
			method:  http.MethodPost, uri: "/v1/admin/roles/r1/implications",
			body: map[string]any{"implies_role_id": "r2"},
		},
		{
			// The edge has no row id: the DELETE addresses it by the IMPLIED
			// role's id under the holder's path, with no body.
			name:    "RemoveRoleImplication",
			respond: `[{"id":"r1","name":"dev"},{"id":"r2","name":"ops"}]`,
			call:    func() (bool, error) { return true, c.RemoveRoleImplication(ctx, "dev", "ops") },
			method:  http.MethodDelete, uri: "/v1/admin/roles/r1/implications/r2",
		},
		{
			// The list carries the lane that wrote each row, scim or admin.
			name:    "Assignments",
			respond: `[{"id":"a1","subject_kind":"user","subject_id":"u1","role_id":"r1","origin":"scim"}]`,
			call: func() (bool, error) {
				list, err := c.Assignments(ctx)
				return len(list) == 1 && list[0].ID == "a1" && list[0].RoleID == "r1" && list[0].Origin == "scim", err
			},
			method: http.MethodGet, uri: "/v1/admin/assignments",
		},
		{
			// Unassign reads the user's own rows with the subject kind and the
			// subject id together, because the server filters only when it
			// has both, and deletes the row of the role. Another user's row
			// of the same role sits first and is never the one deleted.
			name: "Unassign",
			respond: `[{"id":"u1","username":"bob","name":"other"},{"id":"r1","name":"finance","username":"zed"},` +
				`{"id":"a-foreign","subject_kind":"user","subject_id":"u2","role_id":"r1"},{"id":"a-bob","subject_kind":"user","subject_id":"u1","role_id":"r1"}]`,
			call:   func() (bool, error) { return true, c.Unassign(ctx, "bob", "finance") },
			method: http.MethodDelete, uri: "/v1/admin/assignments/a-bob",
		},
		{
			// With no row of the user and the role, Unassign ends on its
			// filtered read and says that the user does not hold the role
			// directly.
			name: "UnassignWithoutARow",
			respond: `[{"id":"u1","username":"bob","name":"other"},{"id":"r1","name":"finance","username":"zed"},` +
				`{"id":"a-foreign","subject_kind":"user","subject_id":"u2","role_id":"r1"}]`,
			call: func() (bool, error) {
				err := c.Unassign(ctx, "bob", "finance")
				return err != nil && strings.Contains(err.Error(), "bob does not hold finance directly, so there is no assignment to remove"), nil
			},
			method: http.MethodGet, uri: "/v1/admin/assignments?subject_kind=user&subject_id=u1",
		},
		{
			name:    "Disconnect",
			respond: `{}`,
			call:    func() (bool, error) { return true, connect.Remove(ctx, c, "github", "") },
			method:  http.MethodDelete, uri: "/v1/connect/github",
		},
		{
			name:    "Approvals",
			respond: `{"approvals":[{"id":"ap1","state":"pending","username":"nova","summary":"shell.exec: deploy-prod"}]}`,
			call: func() (bool, error) {
				list, err := c.Approvals(ctx, "pending")
				return len(list) == 1 && list[0].ID == "ap1" && list[0].State == "pending", err
			},
			method: http.MethodGet, uri: "/v1/admin/approvals?state=pending",
		},
		{
			name:    "ApproveApproval",
			respond: `{"approval":{"id":"ap1","state":"approved","decidedByName":"kim"}}`,
			call: func() (bool, error) {
				rec, err := c.ApproveApproval(ctx, "ap1", "")
				return rec.State == "approved" && rec.DecidedByName == "kim", err
			},
			method: http.MethodPost, uri: "/v1/admin/approvals/ap1/approve",
		},
		{
			name:    "DenyApproval",
			respond: `{"approval":{"id":"ap1","state":"denied"}}`,
			call: func() (bool, error) {
				rec, err := c.DenyApproval(ctx, "ap1", "with-a-reason")
				return rec.State == "denied", err
			},
			method: http.MethodPost, uri: "/v1/admin/approvals/ap1/deny",
			// The 0.63.0 optional reason body; ApproveApproval above pins the
			// reason-less lane (no body at all, the pre-0.63.0 shape).
			body: map[string]any{"reason": "with-a-reason"},
		},
		{
			name:    "ApproverDevices",
			respond: `[{"id":"apd_1","user_id":"u1","username":"kim","name":"kim-pixel","platform":"android","push_routes":1}]`,
			call: func() (bool, error) {
				devs, err := c.ApproverDevices(ctx, "")
				return len(devs) == 1 && devs[0].ID == "apd_1" && devs[0].PushRoutes == 1, err
			},
			method: http.MethodGet, uri: "/v1/admin/approvers",
		},
		{
			name:    "ApproverDevices filtered",
			respond: `[]`,
			call: func() (bool, error) {
				devs, err := c.ApproverDevices(ctx, "kim")
				return len(devs) == 0, err
			},
			method: http.MethodGet, uri: "/v1/admin/approvers?user=kim",
		},
		{
			name:    "RevokeApproverDevice",
			respond: `{"status":"revoked"}`,
			call:    func() (bool, error) { return true, c.RevokeApproverDevice(ctx, "apd_1") },
			method:  http.MethodDelete, uri: "/v1/admin/approvers/apd_1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec.script(tc.respond)
			decoded, err := tc.call()
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if !decoded {
				t.Error("response did not decode into the expected values")
			}
			method, uri, ct, body := rec.last()
			if method != tc.method || uri != tc.uri {
				t.Errorf("wire = %s %s, want %s %s", method, uri, tc.method, tc.uri)
			}
			switch {
			case tc.body != nil:
				if ct != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", ct)
				}
				var got map[string]any
				if err := json.Unmarshal(body, &got); err != nil {
					t.Fatalf("request body %q: %v", body, err)
				}
				if !reflect.DeepEqual(got, tc.body) {
					t.Errorf("body = %v, want %v", got, tc.body)
				}
			case tc.rawBody != "":
				if string(body) != tc.rawBody || ct != tc.rawCT {
					t.Errorf("body = %q (%s), want %q (%s)", body, ct, tc.rawBody, tc.rawCT)
				}
			default:
				if len(body) != 0 {
					t.Errorf("unexpected request body: %s", body)
				}
			}
		})
	}
}

// auditChainServer serves GET /v1/admin/audit from a fixed record set with
// real after-cursor paging, and fails the test if a verification request
// arrives filtered (chain verification must walk the unbroken chain).
func auditChainServer(t *testing.T, recs []AuditRecord) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/admin/audit", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("user") != "" {
			t.Error("VerifyAudit sent a user filter; verification must be unfiltered")
		}
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		page := []AuditRecord{}
		for _, rec := range recs {
			if rec.Seq > after {
				page = append(page, rec)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(page)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return loggedInClient(t, srv.URL)
}

// testChain builds a 3-record chain with real hashes via audit.Link.
func testChain() []AuditRecord {
	ces := []string{`{"n":1}`, `{"n":2}`, `{"n":3}`}
	prev := audit.Genesis
	recs := make([]AuditRecord, len(ces))
	for i, ce := range ces {
		h := audit.Link(prev, ce)
		recs[i] = AuditRecord{Seq: int64(i + 1), CE: ce, PrevHash: prev, Hash: h}
		prev = h
	}
	return recs
}

// TestVerifyAuditIntactChain pins the happy path: a chain whose hashes were
// computed with the shared audit.Link verifies end to end, across paging.
func TestVerifyAuditIntactChain(t *testing.T) {
	c := auditChainServer(t, testChain())
	ok, count, broken, err := c.VerifyAudit(context.Background())
	if err != nil {
		t.Fatalf("VerifyAudit: %v", err)
	}
	if !ok || count != 3 || broken != 0 {
		t.Errorf("ok=%v count=%d broken=%d, want ok=true count=3 broken=0", ok, count, broken)
	}
}

// TestVerifyAuditTamperedChain pins tamper evidence: rewriting one record's
// CE after the fact breaks verification at exactly that sequence number.
func TestVerifyAuditTamperedChain(t *testing.T) {
	recs := testChain()
	recs[1].CE = `{"n":2,"amount":9999}` // hash no longer covers these bytes
	c := auditChainServer(t, recs)
	ok, _, broken, err := c.VerifyAudit(context.Background())
	if err != nil {
		t.Fatalf("VerifyAudit: %v", err)
	}
	if ok || broken != 2 {
		t.Errorf("ok=%v broken=%d, want ok=false broken=2", ok, broken)
	}
}

// TestDoSurfacesAPIError pins the error contract of Do: a non-2xx answer
// surfaces the server's error string, or "HTTP <code>" when the body has none.
func TestDoSurfacesAPIError(t *testing.T) {
	var mu sync.Mutex
	status, body := http.StatusForbidden, `{"error":"forbidden: admin role required"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		code, resp := status, body
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(srv.Close)
	c := loggedInClient(t, srv.URL)

	err := c.RemoveApp(context.Background(), "gh")
	if err == nil || err.Error() != "forbidden: admin role required" {
		t.Errorf("err = %v, want the server's error string", err)
	}

	mu.Lock()
	status, body = http.StatusInternalServerError, ""
	mu.Unlock()
	if err := c.UnbindTools(context.Background(), "b1"); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("err = %v, want HTTP 500 fallback", err)
	}
}
