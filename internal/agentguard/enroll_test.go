package agentguard

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// TestEnrollPrintsWhatTheServiceStreams pins the enroll output the journey
// matrix and people read: the code line keeps the "confirm code XXXX-XXXX"
// shape test/e2e-matrix matches, notes pass through, and the result names
// who enrolled. An error event comes back as its sentence.
func TestEnrollPrintsWhatTheServiceStreams(t *testing.T) {
	codeRE := regexp.MustCompile(`confirm code ([A-Z2-9]{4}-[A-Z2-9]{4})`)
	tests := []struct {
		name    string
		events  []localapi.EnrollEvent
		want    []string
		wantErr string
	}{
		{name: "a person", events: []localapi.EnrollEvent{
			{Event: localapi.EnrollCode, UserCode: "ABCD-EFGH", VerificationURI: "https://idp.example/device?code=ABCD-EFGH"},
			{Event: localapi.EnrollNote, Text: "waiting for approval in the browser…"},
			{Event: localapi.EnrollDone, Result: &localapi.EnrollResult{Kind: localapi.KindPerson, Username: "alice", DeviceID: "dev-7f3a"}},
		}, want: []string{
			"To authorize this device, open:\n  https://idp.example/device?code=ABCD-EFGH\nand confirm code ABCD-EFGH\n",
			"waiting for approval in the browser…\n",
			"Enrolled as alice (device dev-7f3a). The Straza background service holds this machine's key.\n",
		}},
		{name: "an agent", events: []localapi.EnrollEvent{
			{Event: localapi.EnrollDone, Result: &localapi.EnrollResult{Kind: localapi.KindAgent, Username: "ci-bot"}},
		}, want: []string{"Enrolled headless as ci-bot. The Straza background service holds this agent's key and starts each of its sessions.\n"}},
		{name: "a refusal", events: []localapi.EnrollEvent{
			{Event: localapi.EnrollError, Error: "This machine isn't enrolled for you yet. Run `straza enroll` and sign in with your work account."},
		}, wantErr: "This machine isn't enrolled for you yet. Run `straza enroll` and sign in with your work account."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newFakeService(t)
			svc.enroll = tt.events
			var out bytes.Buffer
			err := Enroll(context.Background(), NewServiceClient(), localapi.EnrollRequest{ServerURL: "https://straza.example"}, &out)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, want := out.String(), strings.Join(tt.want, ""); got != want {
				t.Errorf("output =\n%q\nwant\n%q", got, want)
			}
			if tt.events[0].Event == localapi.EnrollCode && codeRE.FindStringSubmatch(out.String()) == nil {
				t.Errorf("output %q does not match the matrix's code line", out.String())
			}
		})
	}
}

// TestKeygenOutput pins the keygen contract five parsers depend on: the
// exact "Public key: <base64>" line, the registration lines, and a non-zero
// failure carrying the service's sentence when a key exists and --force was
// not given.
func TestKeygenOutput(t *testing.T) {
	svc := newFakeService(t)
	var out bytes.Buffer
	if err := Keygen(context.Background(), NewServiceClient(), "ci-bot", false, &out); err != nil {
		t.Fatal(err)
	}
	want := "NHI key generated (the Straza background service holds the private half).\n" +
		"Public key: cHVibGljLWtleQ==\n\n" +
		"An administrator registers it with:\n  strazactl users nhi-key set ci-bot cHVibGljLWtleQ==\n" +
		"(or: PUT /v1/admin/users/{id}/nhi-key {\"public_key\":\"cHVibGljLWtleQ==\"})\n" +
		"Then enroll headless:\n  straza enroll --server <strazad-url> --headless --user ci-bot\n"
	if out.String() != want {
		t.Errorf("keygen output =\n%q\nwant\n%q", out.String(), want)
	}

	svc.keyHeld = true
	err := Keygen(context.Background(), NewServiceClient(), "ci-bot", false, &bytes.Buffer{})
	if err == nil || err.Error() != "An agent key already exists for you. Pass --force to replace it." {
		t.Fatalf("keygen over a held key = %v, want the service's refusal", err)
	}
	if err := Keygen(context.Background(), NewServiceClient(), "ci-bot", true, &bytes.Buffer{}); err != nil {
		t.Fatalf("keygen --force = %v", err)
	}
}

// TestEnrollOpensTheBrowser pins the browser lane of the thin client: the
// request names the lane, the browser event runs $BROWSER with the link as
// its only argument, and the person reads the opened sentence with the
// link before the result.
func TestEnrollOpensTheBrowser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the recorder is a shell script")
	}
	dir := t.TempDir()
	record := filepath.Join(dir, "link")
	script := filepath.Join(dir, "browser")
	body := "#!/bin/sh\nprintf '%s %s' \"$#\" \"$1\" > " + record + ".tmp && mv " + record + ".tmp " + record + "\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BROWSER", script)
	const link = "https://idp.example/realms/straza/protocol/openid-connect/auth?client_id=straza&state=s1"
	svc := newFakeService(t)
	svc.enroll = []localapi.EnrollEvent{
		{Event: localapi.EnrollBrowser, VerificationURI: link},
		{Event: localapi.EnrollDone, Result: &localapi.EnrollResult{Kind: localapi.KindPerson, Username: "alice", DeviceID: "dev-7f3a"}},
	}
	var out bytes.Buffer
	req := localapi.EnrollRequest{ServerURL: "https://straza.example", Lane: localapi.LaneBrowser}
	if err := Enroll(context.Background(), NewServiceClient(), req, &out); err != nil {
		t.Fatal(err)
	}
	want := "Straza opened your browser to sign in at https://idp.example. If no browser opened, open this link on this computer:\n  " + link + "\n" +
		"Enrolled as alice (device dev-7f3a). The Straza background service holds this machine's key.\n"
	if out.String() != want {
		t.Errorf("output =\n%q\nwant\n%q", out.String(), want)
	}
	if len(svc.enrolls) != 1 || svc.enrolls[0].Lane != localapi.LaneBrowser {
		t.Errorf("service got %+v, want one request on the browser lane", svc.enrolls)
	}
	// The browser runs in the background, and macOS may check a script it
	// has not seen before it first runs, so the wait allows ten seconds.
	var got []byte
	for range 500 {
		if b, err := os.ReadFile(record); err == nil {
			got = b
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if string(got) != "1 "+link {
		t.Fatalf("BROWSER got %q, want the link as its only argument", got)
	}
}
