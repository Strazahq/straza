package ctl

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"
)

// CatalogSubject is the resolved principal a catalog preview was computed for:
// the username (empty for a pure role preview) plus the roles and groups the
// gateway would attribute to it.
type CatalogSubject struct {
	User   string   `json:"user"`
	Roles  []string `json:"roles"`
	Groups []string `json:"groups"`
}

// CatalogEntry is one row of the effective-catalog preview: how a single tool
// (or, for app-level rows, a whole app) resolves for the previewed subject.
// Tool is empty for app-level statuses (no_binding, not_running); Reason
// carries the winning rule's reason text (or the engine's default
// sentence), else empty. Hint is the server's one plain sentence for the
// row's status, absent on a server that predates it. Status is a raw enum
// value (visible|approve_gated|hidden_policy|no_binding|matcher_miss|
// not_running) kept verbatim so it stays grep-able. RuleID/SetName name the
// rule behind a tool-level outcome (empty when no rule fired), and Default
// marks a decision no rule made (0.71.0).
type CatalogEntry struct {
	App     string `json:"app"`
	Tool    string `json:"tool"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Hint    string `json:"hint,omitempty"`
	RuleID  string `json:"ruleId,omitempty"`
	SetName string `json:"setName,omitempty"`
	Default bool   `json:"default,omitempty"`
}

// CatalogPreview is the admin catalog-preview response: the resolved subject
// and the effective per-tool visibility the gateway would present to it
// (openapi.yaml CatalogPreview schema / GET /v1/admin/catalog/preview).
// Notes carries one sentence per role in the request that does not exist,
// so a preview for a misspelled role is never read as a real one.
type CatalogPreview struct {
	Subject CatalogSubject `json:"subject"`
	Entries []CatalogEntry `json:"entries"`
	Notes   []string       `json:"notes,omitempty"`
}

// CatalogPreview computes the effective tool catalog for a hypothetical
// subject without a live session, the admin "what would this user see?" view.
// roles are repeatable; user resolves the subject from an existing username;
// app filters to one app. At least one of roles or user must be set (the
// server 400s otherwise) and that error surfaces via Do.
func (c *Client) CatalogPreview(ctx context.Context, roles []string, user, app string) (CatalogPreview, error) {
	params := url.Values{}
	for _, r := range roles {
		params.Add("role", r)
	}
	if user != "" {
		params.Set("user", user)
	}
	if app != "" {
		params.Set("app", app)
	}
	var out CatalogPreview
	return out, c.Do(ctx, http.MethodGet, "/v1/admin/catalog/preview?"+params.Encode(), nil, &out)
}

// RenderCatalogPreview writes a catalog preview to w: one subject line (user,
// roles, and groups when present), one note line per server note, then an
// APP/TOOL/STATUS/REASON table. REASON is the server's hint sentence for the
// row, or the rule's reason text on a server that sends no hint. App-level
// rows render with an empty TOOL column.
func RenderCatalogPreview(w io.Writer, p CatalogPreview) error {
	for _, n := range p.Notes {
		fmt.Fprintln(w, "note: "+n)
	}
	parts := []string{"subject:"}
	if p.Subject.User != "" {
		parts = append(parts, "user="+p.Subject.User)
	}
	if len(p.Subject.Roles) > 0 {
		parts = append(parts, "roles="+strings.Join(p.Subject.Roles, ","))
	}
	if len(p.Subject.Groups) > 0 {
		parts = append(parts, "groups="+strings.Join(p.Subject.Groups, ","))
	}
	fmt.Fprintln(w, strings.Join(parts, " "))

	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SERVER\tTOOL\tSTATUS\tREASON")
	for _, e := range p.Entries {
		reason := e.Hint
		if reason == "" {
			reason = e.Reason
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.App, e.Tool, e.Status, reason)
	}
	return tw.Flush()
}
