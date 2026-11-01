package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// TestStatusIdentityLine pins the identity rows of `straza status`: an
// enrolled person names the device, and an agent says it has no device
// instead of printing an empty parenthetical. Both name the key by a short
// thumbprint.
func TestStatusIdentityLine(t *testing.T) {
	tests := []struct {
		name string
		e    localapi.Enrollment
		want string
	}{
		{"enrolled device", localapi.Enrollment{Kind: localapi.KindPerson, Username: "alice", DeviceID: "dev-7f3a", KeyThumbprint: "thumbprint-abcdef"},
			"identity   alice (device dev-7f3a, key thumbprint-a)"},
		{"agent", localapi.Enrollment{Kind: localapi.KindAgent, Username: "ci-bot", KeyThumbprint: "k1"},
			"identity   ci-bot (AI agent, no device, key k1)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := identityLine(tt.e); got != tt.want {
				t.Errorf("identityLine(%+v) = %q, want %q", tt.e, got, tt.want)
			}
		})
	}
}

// TestPrintStatus pins what `straza status` prints from the service's
// status: the service and its custody, the server, one row per live session
// with its kill-switch push, a revoked session's reason, and the audit
// backlog. An unenrolled caller gets the enroll command and nothing else.
func TestPrintStatus(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		st   localapi.Status
		want []string
	}{
		{"not enrolled", localapi.Status{Version: "1.2.0", Custody: localapi.CustodyAccount}, []string{
			"service    straza service 1.2.0 (custody account)",
			"identity   not enrolled (run `straza enroll --server <strazad url>`)",
		}},
		{"two windows, one revoked", localapi.Status{Version: "1.2.0", Custody: localapi.CustodyAccount,
			Enrollment: &localapi.Enrollment{ServerURL: "https://straza.example", Kind: localapi.KindPerson, Username: "alice", DeviceID: "d1", KeyThumbprint: "k1"},
			Sessions: []localapi.SessionStatus{
				{Harness: localapi.Harness{Name: "claude-code", Version: "2.1.0", Session: "w1"}, SessionID: "s1", User: "alice",
					ExpiresAt: now.Add(4 * time.Minute), Push: localapi.PushConnected},
				{Harness: localapi.Harness{Name: "codex"}, SessionID: "s2", User: "alice", Revoked: "an administrator revoked this session"},
			},
			Spool: localapi.SpoolStatus{Pending: 3}}, []string{
			"service    straza service 1.2.0 (custody account)",
			"server     https://straza.example",
			"identity   alice (device d1, key k1)",
			"session    claude-code/2.1.0 s1 as alice (expires in 4m0s, kill switch connected)",
			"session    codex s2 as alice (revoked: an administrator revoked this session)",
			"audit      3 record(s) waiting to upload",
		}},
		{"a week of idle windows", localapi.Status{Version: "1.2.0", Custody: localapi.CustodyAccount,
			Enrollment: &localapi.Enrollment{ServerURL: "https://straza.example", Kind: localapi.KindPerson, Username: "alice", DeviceID: "d1", KeyThumbprint: "k1"},
			Sessions: []localapi.SessionStatus{
				{Harness: localapi.Harness{Name: "claude-code", Session: "w1"}, SessionID: "s1", User: "alice",
					ExpiresAt: now.Add(4 * time.Minute), Push: localapi.PushConnected},
				{Harness: localapi.Harness{Name: "claude-code", Session: "w2"}, SessionID: "s2", User: "alice", ExpiresAt: now.Add(-2 * time.Hour)},
				{Harness: localapi.Harness{Name: "claude-code", Session: "w3"}, SessionID: "s3", User: "alice", ExpiresAt: now.Add(-50 * time.Hour)},
			}}, []string{
			"service    straza service 1.2.0 (custody account)",
			"server     https://straza.example",
			"identity   alice (device d1, key k1)",
			"session    claude-code s1 as alice (expires in 4m0s, kill switch connected)",
			"session    2 windows unused for over an hour (each one checks in again at its next call)",
			"audit      nothing waiting to upload",
		}},
		{"machine changed, no session", localapi.Status{Version: "1.2.0", Custody: localapi.CustodyUser,
			Enrollment: &localapi.Enrollment{ServerURL: "https://straza.example", Kind: localapi.KindAgent, Username: "ci-bot", KeyThumbprint: "k1", MachineChanged: true}}, []string{
			"service    straza service 1.2.0 (custody user)",
			"server     https://straza.example",
			"identity   ci-bot (AI agent, no device, key k1)",
			"warning    This machine's identity changed since it was enrolled, so Straza stopped using its key. Run `straza enroll` to enroll it again.",
			"session    none (start a harness session)",
			"audit      nothing waiting to upload",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			printStatus(&out, tt.st, now)
			if got, want := strings.TrimRight(out.String(), "\n"), strings.Join(tt.want, "\n"); got != want {
				t.Errorf("status =\n%s\nwant\n%s", got, want)
			}
		})
	}
}
