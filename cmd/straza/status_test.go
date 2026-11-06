package main

import (
	"bytes"
	"os"
	"path/filepath"
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

// TestStatusSessionLine pins the row of one session window: a live token
// says how long it lasts, a token that lapsed under an hour ago says how
// long ago, the way straza doctor words it, and that it renews at the next
// call unless the machine changed, which stopped the key that renews it. A
// revoked window gives the reason instead.
func TestStatusSessionLine(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	base := localapi.SessionStatus{Harness: localapi.Harness{Name: "claude-code", Version: "2.1.0"}, SessionID: "s1", User: "alice",
		Push: localapi.PushConnected}
	lapsed := now.Add(-(20*time.Minute + 16*time.Second))
	tests := []struct {
		name    string
		expires time.Time
		revoked string
		changed bool // the enrollment's machine changed
		want    string
	}{
		{"live", now.Add(4 * time.Minute), "", false,
			"session    claude-code/2.1.0 s1 as alice (expires in 4m0s, kill switch connected)"},
		{"expires this second", now, "", false,
			"session    claude-code/2.1.0 s1 as alice (expires in 0s, kill switch connected)"},
		{"lapsed twenty minutes ago", lapsed, "", false,
			"session    claude-code/2.1.0 s1 as alice (token expired 20m16s ago and renews at the next call, kill switch connected)"},
		{"lapsed on a machine that changed", lapsed, "", true,
			"session    claude-code/2.1.0 s1 as alice (token expired 20m16s ago, kill switch connected)"},
		{"revoked", now.Add(-time.Minute), "an administrator revoked this session", false,
			"session    claude-code/2.1.0 s1 as alice (revoked: an administrator revoked this session)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := base
			s.ExpiresAt, s.Revoked = tt.expires, tt.revoked
			if got := sessionLine(s, now, tt.changed); got != tt.want {
				t.Errorf("sessionLine = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestPrintStatus pins what `straza status` prints from the service's
// status: the service and its custody, the server, one row per live session
// with its kill-switch push, a revoked session's reason, and the audit
// backlog. An unenrolled caller gets the enroll commands of a person and an
// agent and nothing else. A changed machine gets the next step of its kind,
// and an agent's names both of its lanes. The enroll commands leave out
// --server when the managed configuration names the server.
func TestPrintStatus(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	const managed = "serverUrl: https://straza.example\n"
	agentChanged := localapi.Status{Version: "1.2.0", Custody: localapi.CustodyUser,
		Enrollment: &localapi.Enrollment{ServerURL: "https://straza.example", Kind: localapi.KindAgent, Username: "ci-bot", KeyThumbprint: "k1", MachineChanged: true}}
	tests := []struct {
		name    string
		managed string // the managed config.yaml, or "" for no managed layout
		st      localapi.Status
		want    []string
	}{
		{"not enrolled", "", localapi.Status{Version: "1.2.0", Custody: localapi.CustodyAccount}, []string{
			"service    straza service 1.2.0 (custody account)",
			"identity   not enrolled. A person runs `straza enroll --server <strazad url>` and signs in with their work account. " +
				"An AI agent runs `straza enroll --server <strazad url> --headless` with its registered key or the client secret IT installed for it.",
		}},
		{"not enrolled, managed", managed, localapi.Status{Version: "1.2.0", Custody: localapi.CustodyAccount}, []string{
			"service    straza service 1.2.0 (custody account)",
			"identity   not enrolled. A person runs `straza enroll` and signs in with their work account. " +
				"An AI agent runs `straza enroll --headless` with its registered key or the client secret IT installed for it.",
		}},
		{"two windows, one revoked", "", localapi.Status{Version: "1.2.0", Custody: localapi.CustodyAccount,
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
		{"a week of idle windows", "", localapi.Status{Version: "1.2.0", Custody: localapi.CustodyAccount,
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
		{"a person's machine changed", "", localapi.Status{Version: "1.2.0", Custody: localapi.CustodyAccount,
			Enrollment: &localapi.Enrollment{ServerURL: "https://straza.example", Kind: localapi.KindPerson, Username: "alice", DeviceID: "d1", KeyThumbprint: "k1", MachineChanged: true}}, []string{
			"service    straza service 1.2.0 (custody account)",
			"server     https://straza.example",
			"identity   alice (device d1, key k1)",
			"warning    This machine's identity changed since it was enrolled, so Straza stopped using its key. Run `straza enroll --server https://straza.example` to enroll it again.",
			"session    none (start a harness session)",
			"audit      nothing waiting to upload",
		}},
		{"an agent's machine changed, no session", "", agentChanged, []string{
			"service    straza service 1.2.0 (custody user)",
			"server     https://straza.example",
			"identity   ci-bot (AI agent, no device, key k1)",
			"warning    This machine's identity changed since it was enrolled, so Straza stopped using its key. " +
				"An agent with a key from `straza keygen` runs `straza keygen --force --user ci-bot`, has an admin register the new public key, " +
				"and then runs `straza enroll --server https://straza.example --headless`. " +
				"An agent that signs in with a client secret runs `straza enroll --server https://straza.example --headless --user ci-bot`.",
			"session    none (start a harness session)",
			"audit      nothing waiting to upload",
		}},
		{"an agent's machine changed, managed", managed, agentChanged, []string{
			"service    straza service 1.2.0 (custody user)",
			"server     https://straza.example",
			"identity   ci-bot (AI agent, no device, key k1)",
			"warning    This machine's identity changed since it was enrolled, so Straza stopped using its key. " +
				"An agent with a key from `straza keygen` runs `straza keygen --force --user ci-bot`, has an admin register the new public key, " +
				"and then runs `straza enroll --headless`. " +
				"An agent that signs in with a client secret runs `straza enroll --headless --user ci-bot`.",
			"session    none (start a harness session)",
			"audit      nothing waiting to upload",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sys := t.TempDir()
			t.Setenv("STRAZA_SYSTEM", sys)
			if tt.managed != "" {
				if err := os.WriteFile(filepath.Join(sys, "config.yaml"), []byte(tt.managed), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			printStatus(&out, tt.st, now)
			if got, want := strings.TrimRight(out.String(), "\n"), strings.Join(tt.want, "\n"); got != want {
				t.Errorf("status =\n%s\nwant\n%s", got, want)
			}
		})
	}
}
