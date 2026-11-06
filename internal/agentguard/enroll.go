package agentguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/oidcflow"
)

// Enroll asks the Straza background service to enroll the caller at
// req.ServerURL: a person at the issuer strazad advertises, by the browser
// sign-in or by device code as req.Lane says, or with req.Headless set the
// AI agent req.Username with the key or client secret the service holds for
// it. The service keeps the key and every token. Enroll prints what the
// service streams to w: the code and link to open, or the link it opens in
// the person's browser, its progress notes, and the result.
func Enroll(ctx context.Context, svc *localapi.Client, req localapi.EnrollRequest, w io.Writer) error {
	res, err := svc.Enroll(ctx, req, func(ev localapi.EnrollEvent) {
		switch ev.Event {
		case localapi.EnrollCode:
			fmt.Fprintf(w, "To authorize this device, open:\n  %s\nand confirm code %s\n", ev.VerificationURI, ev.UserCode)
		case localapi.EnrollBrowser:
			oidcflow.OpenBrowser(w, ev.VerificationURI)
		case localapi.EnrollNote:
			fmt.Fprintln(w, ev.Text)
		}
	})
	if err != nil {
		return errors.New(ServiceSentence(err))
	}
	if res.Kind == localapi.KindAgent {
		fmt.Fprintf(w, "Enrolled headless as %s. The Straza background service holds this agent's key and starts each of its sessions.\n", res.Username)
		return nil
	}
	fmt.Fprintf(w, "Enrolled as %s (device %s). The Straza background service holds this machine's key.\n", res.Username, res.DeviceID)
	return nil
}

// Keygen asks the service to make the caller's agent key and prints the
// public half plus the registration an administrator runs. The private half
// never leaves the service. The "Public key: " line is parsed by scripts, so
// its shape is fixed.
func Keygen(ctx context.Context, svc *localapi.Client, username string, force bool, w io.Writer) error {
	ans, err := svc.Keygen(ctx, localapi.KeygenRequest{Username: username, Force: force})
	if err != nil {
		return errors.New(ServiceSentence(err))
	}
	name := ans.Username
	if name == "" {
		name = username
	}
	if name == "" {
		name = "<nhi-username>"
	}
	fmt.Fprintln(w, "NHI key generated (the Straza background service holds the private half).")
	fmt.Fprintf(w, "Public key: %s\n\n", ans.PublicKey)
	fmt.Fprintf(w, "An administrator registers it with:\n  strazactl users nhi-key set %s %s\n", name, ans.PublicKey)
	fmt.Fprintf(w, "(or: PUT /v1/admin/users/{id}/nhi-key {\"public_key\":\"%s\"})\n", ans.PublicKey)
	server := "--server <strazad-url> "
	if managedServer() != "" {
		server = ""
	}
	fmt.Fprintf(w, "Then enroll headless:\n  straza enroll %s--headless --user %s\n", server, name)
	return nil
}

// managedServer returns the server that this machine's managed
// configuration names, or "" when there is no managed layout or its
// configuration names none. The service enrolls at that server whatever
// --server says, so the enroll line leaves the flag out then.
func managedServer() string {
	if !ManagedInstalled() {
		return ""
	}
	raw, err := os.ReadFile(ManagedConfigPath()) // #nosec G304 -- fixed managed layout path
	if err != nil {
		return ""
	}
	var c Config
	if yaml.Unmarshal(raw, &c) != nil {
		return ""
	}
	return c.ServerURL
}
