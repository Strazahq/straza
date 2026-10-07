package server

import (
	"context"
	"net/http"
)

// straza.audit.authn producer (spec/events rev 21): login and session-end
// outcomes on the hash chain. Every helper here honors the rev 21 honesty
// rules: user/userId/session/harness appear only when the producer actually
// knows them (never fabricated), and sourceIp/userAgent ride ONLY when a
// client connection produced the event (the documented doctrine exception,
// scoped to authn events), so a janitor event's missing sourceIp means
// exactly "no client connection".

// maxUserAgentBytes caps the recorded User-Agent (rev 21): the header is
// attacker-controlled input headed for the chain and every SIEM sink.
const maxUserAgentBytes = 256

// authnFields are the when-known payload fields; zero values are omitted
// from the wire entirely.
type authnFields struct {
	Via     string // login only: id-token|session-token|device-token|password
	User    string // username
	UserID  string
	Session string
	Harness string // name/version form, e.g. "approvals/1"
	Reason  string // failures and session ends
	// ApprovalID names the request a refused decision tried to decide.
	ApprovalID string
}

// authnPayload assembles the event data. r nil means no client connection
// produced the event (janitor): no sourceIp, no userAgent.
func authnPayload(action, outcome string, f authnFields, r *http.Request) map[string]any {
	data := map[string]any{"action": action, "outcome": outcome}
	if f.Via != "" {
		data["via"] = f.Via
	}
	if f.User != "" {
		data["user"] = f.User
	}
	if f.UserID != "" {
		data["userId"] = f.UserID
	}
	if f.Session != "" {
		data["session"] = f.Session
	}
	if f.Harness != "" {
		data["harness"] = f.Harness
	}
	if f.Reason != "" {
		data["reason"] = f.Reason
	}
	if f.ApprovalID != "" {
		data["approvalId"] = f.ApprovalID
	}
	if r != nil {
		// Transport peer only, never X-Forwarded-For: the spoofable header
		// must not become chained "evidence" (same rule as the rate limiter).
		data["sourceIp"] = clientIP(r)
		if ua := r.UserAgent(); ua != "" {
			if len(ua) > maxUserAgentBytes {
				ua = ua[:maxUserAgentBytes]
			}
			data["userAgent"] = ua
		}
	}
	return data
}

// emitAuthnLogin records a login outcome (success|failure) produced by the
// client request r.
func (a *App) emitAuthnLogin(r *http.Request, outcome string, f authnFields) {
	a.emitEventCtx(r.Context(), "straza.audit.authn", authnPayload("login", outcome, f, r))
}

// emitAuthnSessionEnd records a session.end outcome
// (revoked-self|revoked-admin|idle-closed|lifetime-closed). r is the client
// request when one produced the event, nil for the janitor.
func (a *App) emitAuthnSessionEnd(ctx context.Context, r *http.Request, outcome string, f authnFields) {
	a.emitEventCtx(ctx, "straza.audit.authn", authnPayload("session.end", outcome, f, r))
}

// harnessLabel renders the name/version wire form, "" when the request
// carried no harness name (never fabricated).
func harnessLabel(h harnessInfo) string {
	if h.Name == "" {
		return ""
	}
	return joinHarness(h.Name, h.Version)
}

// joinHarness renders the name/version form of a harness for records and
// claims, and the bare name when the harness reported no version, so a
// record never ends in a dangling slash.
func joinHarness(name, version string) string {
	if version == "" {
		return name
	}
	return name + "/" + version
}
