package ctl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCatalogPreviewWire pins the wrapper to the wire contract of GET
// /v1/admin/catalog/preview: verb, escaped path, and query serialization
// (including repeatable role= params), and that the canned response decodes
// into CatalogPreview.
func TestCatalogPreviewWire(t *testing.T) {
	rec := &wireRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(srv.Close)
	c := loggedInClient(t, srv.URL)
	ctx := context.Background()

	// url.Values.Encode sorts keys, so app < role < user is canonical; values
	// under a repeated key keep insertion order (dev before ops).
	canned := `{"subject":{"user":"bob","roles":["dev","ops"],"groups":["eng"]},` +
		`"entries":[{"app":"midpoint","tool":"users.create","status":"visible","reason":""}]}`

	tests := []struct {
		name  string
		roles []string
		user  string
		app   string
		uri   string
	}{
		{
			name:  "role only",
			roles: []string{"dev"},
			uri:   "/v1/admin/catalog/preview?role=dev",
		},
		{
			name:  "repeatable roles serialize as repeated params",
			roles: []string{"dev", "ops"},
			uri:   "/v1/admin/catalog/preview?role=dev&role=ops",
		},
		{
			name: "user only",
			user: "bob",
			uri:  "/v1/admin/catalog/preview?user=bob",
		},
		{
			name:  "roles plus user and app filter",
			roles: []string{"dev", "ops"},
			user:  "bob",
			app:   "midpoint",
			uri:   "/v1/admin/catalog/preview?app=midpoint&role=dev&role=ops&user=bob",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec.script(canned)
			p, err := c.CatalogPreview(ctx, tc.roles, tc.user, tc.app)
			if err != nil {
				t.Fatalf("CatalogPreview: %v", err)
			}
			method, uri, _, body := rec.last()
			if method != http.MethodGet || uri != tc.uri {
				t.Errorf("wire = %s %s, want GET %s", method, uri, tc.uri)
			}
			if len(body) != 0 {
				t.Errorf("unexpected request body: %s", body)
			}
			if p.Subject.User != "bob" || len(p.Subject.Roles) != 2 ||
				len(p.Entries) != 1 || p.Entries[0].App != "midpoint" || p.Entries[0].Status != "visible" {
				t.Errorf("response did not decode into the expected values: %+v", p)
			}
		})
	}
}

// TestRenderCatalogPreview pins the rendered shape: the subject line carries
// user/roles/groups, statuses stay the raw enum values, REASON is the
// server's hint when it sends one and the rule reason otherwise, and
// app-level rows (no_binding, not_running) render with an empty TOOL column.
func TestRenderCatalogPreview(t *testing.T) {
	p := CatalogPreview{
		Subject: CatalogSubject{User: "bob", Roles: []string{"dev", "ops"}, Groups: []string{"eng"}},
		Entries: []CatalogEntry{
			{App: "midpoint", Tool: "users.create", Status: "visible", Hint: "has access, no policy gates it"},
			{App: "midpoint", Tool: "users.delete", Status: "approve_gated"},
			{App: "vault", Tool: "secrets.read", Status: "hidden_policy", Reason: "role dev denied by rule no-prod-secrets"},
			{App: "vault", Status: "no_binding"},   // app-level row: empty tool
			{App: "legacy", Status: "not_running"}, // app-level row: empty tool
		},
	}
	var out strings.Builder
	if err := RenderCatalogPreview(&out, p); err != nil {
		t.Fatalf("RenderCatalogPreview: %v", err)
	}
	got := out.String()

	subjectLine := strings.SplitN(got, "\n", 2)[0]
	for _, want := range []string{"user=bob", "roles=dev,ops", "groups=eng"} {
		if !strings.Contains(subjectLine, want) {
			t.Errorf("subject line missing %q:\n%s", want, subjectLine)
		}
	}

	for _, want := range []string{
		"SERVER", "TOOL", "STATUS", "REASON",
		"visible", "approve_gated", "hidden_policy", "no_binding", "not_running",
		"role dev denied by rule no-prod-secrets",
		"has access, no policy gates it",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}

	// An app-level row is exactly app + status: no tool name, no reason.
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "no_binding") {
			fields := strings.Fields(line)
			if len(fields) != 2 || fields[0] != "vault" || fields[1] != "no_binding" {
				t.Errorf("no_binding app-level row should be app + status with empty tool/reason, got fields %v", fields)
			}
		}
	}
}

// TestRenderCatalogPreviewNotes pins that a server note about a role that
// does not exist is printed before the subject line, so a preview for a
// misspelled role is never read as a real one, and that the server's hint
// wins over the rule reason when both are present.
func TestRenderCatalogPreviewNotes(t *testing.T) {
	p := CatalogPreview{
		Subject: CatalogSubject{Roles: []string{"scout-rol"}},
		Notes:   []string{"no role named scout-rol exists; this preview is for a hypothetical subject."},
		Entries: []CatalogEntry{
			{App: "scout-tools", Tool: "get-env", Status: "matcher_miss", Hint: "not in the role's access row on this server"},
			{App: "scout-tools", Tool: "get-sum", Status: "hidden_policy", Reason: "engine reason", Hint: "has access, but scout-role-access rule block-get-sum denies it"},
		},
	}
	var out strings.Builder
	if err := RenderCatalogPreview(&out, p); err != nil {
		t.Fatalf("RenderCatalogPreview: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if lines[0] != "note: no role named scout-rol exists; this preview is for a hypothetical subject." {
		t.Errorf("first line = %q, want the note", lines[0])
	}
	if lines[1] != "subject: roles=scout-rol" {
		t.Errorf("second line = %q, want the subject", lines[1])
	}
	got := out.String()
	for _, want := range []string{"not in the role's access row on this server", "rule block-get-sum denies it"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "engine reason") {
		t.Errorf("the rule reason must not print when the server sent a hint:\n%s", got)
	}
}

// TestCatalogPreviewErrorSurfaces pins that a non-2xx answer (the server's
// role-or-user-required 400) surfaces its error string verbatim.
func TestCatalogPreviewErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"at least one of role or user is required"}`))
	}))
	t.Cleanup(srv.Close)
	c := loggedInClient(t, srv.URL)

	_, err := c.CatalogPreview(context.Background(), nil, "", "")
	if err == nil || err.Error() != "at least one of role or user is required" {
		t.Fatalf("err = %v, want the server's 400 message surfaced verbatim", err)
	}
}
