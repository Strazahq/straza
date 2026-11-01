package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"

	"github.com/strazahq/straza/internal/dpop"
)

// enrollBody is the key enrollment request of /v1/enroll.
type enrollBody struct {
	IDToken        string       `json:"id_token"`
	ClientKind     string       `json:"client_kind"`
	Device         enrollDevice `json:"device"`
	Custody        custody      `json:"custody"`
	MachineBinding string       `json:"machine_binding"`
	Counter        int64        `json:"counter"`
}

// enrollDevice names the device being enrolled.
type enrollDevice struct {
	Name        string `json:"name"`
	Platform    string `json:"platform"`
	Fingerprint string `json:"fingerprint"`
}

// custody says where the key is held.
type custody struct {
	Kind     string `json:"kind"`
	Platform string `json:"platform"`
	Version  string `json:"version"`
}

// enrollAnswer is the part of the enrollment answer the client keeps.
type enrollAnswer struct {
	UserID        string `json:"user_id"`
	Username      string `json:"username"`
	DeviceID      string `json:"device_id"`
	KeyThumbprint string `json:"key_thumbprint"`
}

// errOldServer is the refusal of an enrollment answer without key_thumbprint.
var errOldServer = errors.New("the server enrolled this machine without returning key_thumbprint, " +
	"so it is older than the release that added device key enrollment (openapi 0.143.0) and made a bearer device instead. " +
	"Use a server that supports key enrollment, and revoke the bearer device it made")

// runEnroll enrolls the key in the key file with a key enrollment and
// records the device in the state file.
func runEnroll(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	server := fs.String("server", "", "the strazad base URL, for example http://127.0.0.1:8420")
	idToken := fs.String("id-token", "", "the ID token of a device-flow sign-in")
	idTokenFile := fs.String("id-token-file", "", "a file that holds the ID token")
	keyPath := fs.String("key", "", "the Ed25519 key file, which every enrollment replaces with a new key, mode 0600")
	statePath := fs.String("state", "", "the state file")
	name := fs.String("name", "dpopclient", "the device name")
	fingerprint := fs.String("fingerprint", "", "the device fingerprint, which a re-enrollment repeats")
	custodyKind := fs.String("custody", "user", "where the key is held: account or user")
	binding := fs.String("binding", "", "the machine binding value, 1 to 128 bytes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	token, err := idTokenValue(*idToken, *idTokenFile)
	if err != nil {
		return err
	}
	switch {
	case *server == "" || *keyPath == "" || *statePath == "":
		return errors.New("enroll needs -server, -key and -state")
	case *fingerprint == "" || *binding == "":
		return errors.New("enroll needs -fingerprint and -binding, the values a re-enrollment of this machine repeats")
	case *custodyKind != "account" && *custodyKind != "user":
		return fmt.Errorf("-custody is %q, and it must be account or user", *custodyKind)
	}
	// Each key enrollment brings a new key, because the server refuses a key
	// already enrolled for the user. The key file is replaced only once the
	// server has bound the new key, so a refused enrollment keeps the old one.
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("make a key: %w", err)
	}
	st, err := loadState(*statePath, true)
	if err != nil {
		return err
	}
	if st.Server != strings.TrimRight(*server, "/") {
		st = state{Server: strings.TrimRight(*server, "/")}
	}
	body, err := json.Marshal(enrollBody{
		IDToken: token, ClientKind: "kit",
		Device:  enrollDevice{Name: *name, Platform: runtime.GOOS, Fingerprint: *fingerprint},
		Custody: custody{Kind: *custodyKind, Platform: runtime.GOOS + "/" + runtime.GOARCH, Version: "dpopclient"},
		// A new key starts at counter 0, and each check-in sends a higher one.
		MachineBinding: *binding, Counter: 0,
	})
	if err != nil {
		return fmt.Errorf("encode the enrollment: %w", err)
	}
	c := newClient(&st, key, out)
	a, _, err := c.do(request{method: http.MethodPost, path: "/v1/enroll", body: body})
	if err == nil {
		err = keepEnrollment(&st, a, dpop.Thumbprint(key.Public().(ed25519.PublicKey)))
	}
	if err == nil {
		if werr := writeKey(*keyPath, key); werr != nil {
			err = fmt.Errorf("the server enrolled the device %s, and its key could not be saved, so revoke that device and enroll again: %w", st.DeviceID, werr)
		}
	}
	return save(*statePath, &st, err)
}

// keepEnrollment records an accepted key enrollment in st. It refuses a
// refusal, an answer without key_thumbprint and a thumbprint of another key.
func keepEnrollment(st *state, a answer, want string) error {
	if a.status != http.StatusOK {
		return fmt.Errorf("the enrollment was refused with %d, and the answer above says why", a.status)
	}
	var got enrollAnswer
	if err := json.Unmarshal(a.body, &got); err != nil {
		return fmt.Errorf("the enrollment answer is not JSON: %w", err)
	}
	if got.KeyThumbprint == "" {
		return errOldServer
	}
	if got.KeyThumbprint != want {
		return fmt.Errorf("the server bound the key %s, and the new key is %s, so the key file was not replaced. Enroll again", got.KeyThumbprint, want)
	}
	*st = state{Server: st.Server, Nonce: st.Nonce, UserID: got.UserID, Username: got.Username,
		DeviceID: got.DeviceID, KeyThumbprint: got.KeyThumbprint}
	return nil
}

// idTokenValue returns the ID token from the flag or from the file, which
// keeps it out of the shell history.
func idTokenValue(flagValue, file string) (string, error) {
	switch {
	case flagValue != "" && file != "":
		return "", errors.New("give the ID token once, with -id-token or with -id-token-file")
	case flagValue != "":
		return flagValue, nil
	case file == "":
		return "", errors.New("enroll needs the ID token of a sign-in, with -id-token-file or -id-token")
	}
	raw, err := os.ReadFile(file) //nolint:gosec // G304: the operator names the token file
	if err != nil {
		return "", fmt.Errorf("read the ID token file %s: %w", file, err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("the ID token file %s is empty", file)
	}
	return token, nil
}
