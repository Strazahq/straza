package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/redact"
)

// captureDoc captures the dev role's conversations in mode, or none when
// mode is empty.
func captureDoc(mode string) string {
	capture := ""
	if mode != "" {
		capture = "  capture: {conversations: true, mode: " + mode + "}\n"
	}
	return "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata: {name: cap}\nspec:\n  match: {roles: [dev]}\n" + capture +
		"  rules: [{id: r1, effect: deny, tools: [shell.exec], command: {denyPatterns: [\"rm -rf *\"]}}]\n"
}

// sha is the content hash a capture record carries for s.
func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// TestCaptureContent pins the order of a captured turn: the hash of the
// full original, then the mode, then the size cap.
func TestCaptureContent(t *testing.T) {
	original := strings.Repeat("a", captureMaxBytes+50)
	stored, truncated, hash := captureContent(original, policy.CaptureModeVerbatim)
	if !truncated || len(stored) != captureMaxBytes || hash != sha(original) {
		t.Errorf("verbatim oversized: truncated=%v len=%d hash ok=%v", truncated, len(stored), hash == sha(original))
	}
	small := "the AWS key is AKIAIOSFODNN7EXAMPLE"
	stored, truncated, hash = captureContent(small, policy.CaptureModeRedact)
	if truncated || strings.Contains(stored, "AKIAIOSFODNN7EXAMPLE") || !strings.Contains(stored, redact.Mark) {
		t.Errorf("redact mode stored %q (truncated %v), want the secret masked", stored, truncated)
	}
	if hash != sha(small) {
		t.Error("redact mode must hash the content before redaction")
	}
}

// TestDecideObservational pins the events that are not tool calls: they
// allow with the session's capture directive and record nothing, even past
// the offline grace. prompt.submit records nothing either, because the
// thin client sends the prompt through the capture route.
func TestDecideObservational(t *testing.T) {
	rows := []struct {
		name    string
		kind    string
		mode    string
		expires time.Duration
	}{
		{"session.end with capture on", policy.EventSessionEnd, policy.CaptureModeRedact, time.Hour},
		{"subagent.stop past the grace", policy.EventSubagentStop, "", -time.Hour},
		{"tool.post", policy.EventToolPost, "", time.Hour},
		{"prompt.submit with capture on", policy.EventPromptSubmit, policy.CaptureModeVerbatim, time.Hour},
		{"prompt.submit past the grace", policy.EventPromptSubmit, policy.CaptureModeRedact, -time.Hour},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := gateDouble(t, 0, row.expires, Renewal{})
			f.engine = apiEngine(t, captureDoc(row.mode), 0)
			a, _ := testAPI(f, false)
			req := shellReq("")
			req.Event, req.AgentType, req.AgentID = policy.Event{Kind: row.kind}, "researcher", "a-1"
			ans := decideVia(t, a, req)
			if d := ans.Decision; d.Effect != policy.EffectAllow || !d.Default || ans.Journal.FailClosed || ans.Journal.Spooled {
				t.Fatalf("decision = %+v, want the observational allow", ans)
			}
			if ans.Capture.Conversations != (row.mode != "") || ans.Capture.Mode != row.mode {
				t.Errorf("capture = %+v, want mode %q", ans.Capture, row.mode)
			}
			if recs := f.recorded(); len(recs) != 0 || len(f.decideCalls) != 0 {
				t.Fatalf("%d records and %d decide calls, want none", len(recs), len(f.decideCalls))
			}
		})
	}
}

// TestCaptureRoute pins POST capture: the service applies the session's
// own capture mode to the text the thin client read, records a prompt or a
// reply with the delegate tag, and records nothing when the session's
// policy does not capture or cannot be had.
func TestCaptureRoute(t *testing.T) {
	const reply = "the key is AKIAIOSFODNN7EXAMPLE"
	h := localapi.Harness{Name: "claude-code", Version: "2.1.0", Session: "w1"}
	rows := []struct {
		name      string
		mode      string
		policyErr error
		kind      string
		stored    string
	}{
		{"a reply in redact mode", policy.CaptureModeRedact, nil, localapi.CaptureReply, "the key is " + redact.Mark},
		{"a prompt in redact mode", policy.CaptureModeRedact, nil, localapi.CapturePrompt, "the key is " + redact.Mark},
		{"a prompt word for word", policy.CaptureModeVerbatim, nil, localapi.CapturePrompt, reply},
		{"capture off", "", nil, localapi.CaptureReply, ""},
		{"a snapshot that cannot be had", policy.CaptureModeVerbatim, errors.New("the policy snapshot is missing"), localapi.CaptureReply, ""},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := gateDouble(t, 0, time.Hour, Renewal{})
			f.engine, f.policyErr = apiEngine(t, captureDoc(row.mode), 0), row.policyErr
			a, _ := testAPI(f, false)
			w := serveAs(a, "1000", http.MethodPost, localapi.RouteCapture, localapi.CaptureRequest{
				Harness: h, Kind: row.kind, Text: reply, AgentType: "researcher", AgentID: "a-1"})
			if w.Code != http.StatusNoContent {
				t.Fatalf("capture = %d %q, want 204", w.Code, w.Body.String())
			}
			recs := f.recorded()
			if row.stored == "" {
				if len(recs) != 0 {
					t.Fatalf("%d records, want none", len(recs))
				}
				return
			}
			if len(recs) != 1 || recs[0].ceType != "straza.audit."+row.kind || recs[0].data["content"] != row.stored ||
				recs[0].data["contentHash"] != sha(reply) || recs[0].data["mode"] != row.mode || recs[0].data["truncated"] != false ||
				recs[0].data["session"] != "s-1" || recs[0].data["harness"] != "claude-code/2.1.0" ||
				recs[0].data["agentType"] != "researcher" || recs[0].data["agentId"] != "a-1" || recs[0].caller.UID != "1000" {
				t.Errorf("records = %+v, want one %s in uid 1000's spool", recs, row.kind)
			}
			if c := f.sessionCalls; len(c) != 1 || c[0].start || c[0].harness != h {
				t.Errorf("Session calls = %+v, want one lookup of the window that starts nothing", c)
			}
		})
	}
	t.Run("a spool that refuses the record", func(t *testing.T) {
		f := gateDouble(t, 0, time.Hour, Renewal{})
		f.engine, f.recordErr = apiEngine(t, captureDoc(policy.CaptureModeVerbatim), 0), errors.New("spool: disk full")
		a, _ := testAPI(f, false)
		w := serveAs(a, "1000", http.MethodPost, localapi.RouteCapture, localapi.CaptureRequest{Harness: h, Kind: localapi.CaptureReply, Text: reply})
		if w.Code != http.StatusInternalServerError || !strings.Contains(refusalOf(t, w), "could not write the captured reply") {
			t.Errorf("capture = %d %q, want the 500 sentence", w.Code, w.Body.String())
		}
	})
}

// TestCaptureRedactsSecrets pins what redact mode masks in a captured turn
// and what it keeps, and that word-for-word mode keeps the text as it is,
// which is the positive control for every mask.
func TestCaptureRedactsSecrets(t *testing.T) {
	for _, row := range []struct {
		name, in string
		gone     string // must not survive redact mode
		kept     string // must survive it
	}{
		{"aws key", "creds: AKIAIOSFODNN7EXAMPLE ok", "AKIAIOSFODNN7EXAMPLE", "creds:"},
		{"github pat", "the token is ghp_abcdefghijklmnopqrstuvwxyz0123456789 here", "ghp_abcdef", "the token is"},
		{"bearer header", "Authorization: Bearer eyJhbGciOiJFUzI1NiJ9.payload.sig", "eyJhbGci", "Authorization:"},
		{"pem block", "key:\n-----BEGIN PRIVATE KEY-----\nMIIEvg==\n-----END PRIVATE KEY-----\ndone", "MIIEvg", "done"},
		{"straza api token", "token wat_0123456789abcdefghijklmnopqrstuvwxyzABCDEF used", "wat_0123", "used"},
		{"plain text untouched", "deploy the staging branch", "", "deploy the staging branch"},
	} {
		t.Run(row.name, func(t *testing.T) {
			if stored, _, _ := captureContent(row.in, policy.CaptureModeVerbatim); stored != row.in {
				t.Errorf("verbatim stored %q, want the text as it is", stored)
			}
			stored, _, hash := captureContent(row.in, policy.CaptureModeRedact)
			if row.gone != "" && (strings.Contains(stored, row.gone) || !strings.Contains(stored, redact.Mark)) {
				t.Errorf("redact stored %q, want %q masked", stored, row.gone)
			}
			if !strings.Contains(stored, row.kept) || hash != sha(row.in) {
				t.Errorf("redact stored %q with hash %s, want %q kept and the original's hash", stored, hash, row.kept)
			}
		})
	}
}
