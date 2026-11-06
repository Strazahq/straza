package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/dpop"
	"github.com/strazahq/straza/internal/oidcflow"
	"github.com/strazahq/straza/internal/version"
)

// enrollBody is the key enrollment of /v1/enroll. Its DPoP proof comes from
// the key being enrolled.
type enrollBody struct {
	IDToken        string      `json:"id_token"`
	ClientKind     string      `json:"client_kind"`
	Device         deviceBody  `json:"device"`
	Custody        custodyBody `json:"custody"`
	MachineBinding string      `json:"machine_binding"`
	Counter        int64       `json:"counter"`
}

// deviceBody names the machine being enrolled.
type deviceBody struct {
	Name        string `json:"name"`
	Platform    string `json:"platform"`
	Fingerprint string `json:"fingerprint"`
}

// custodyBody says who holds the key: the service under its own account,
// or under the person's own.
type custodyBody struct {
	Kind     string `json:"kind"`
	Platform string `json:"platform"`
	Version  string `json:"version"`
}

// enrollAnswer is the part of a key enrollment's answer the service keeps.
type enrollAnswer struct {
	UserID          string `json:"user_id"`
	Username        string `json:"username"`
	DeviceID        string `json:"device_id"`
	KeyThumbprint   string `json:"key_thumbprint"`
	SignInExpiresIn int    `json:"sign_in_expires_in"`
}

// errDeviceCodeExpired is a device flow nobody approved in time.
var errDeviceCodeExpired = errors.New("the sign-in code expired before it was approved")

// deviceCodeExpired is errDeviceCodeExpired at server, with the enroll
// command that works on this machine.
func deviceCodeExpired(server string) error {
	return fmt.Errorf("%w. Run `%s` again and approve the new code it shows", errDeviceCodeExpired, enrollCommand(server))
}

// codeLaneRefused is the answer to a device-code enrollment at server
// whose idp.json says it accepts the browser lane only.
func codeLaneRefused(server string) string {
	return "Straza refused this sign-in because your Straza admin requires a browser sign-in, " +
		"and a sign-in code cannot show which machine it was approved for. Run `" + enrollCommand(server) +
		"` in a desktop session on this machine."
}

// enrollCommand is the straza enroll command that enrolls this machine at
// server: the bare command when the managed configuration names a server,
// because the service enrolls there then, and the command with --server
// otherwise, because the service refuses an enrollment that names no
// server.
func enrollCommand(server string) string {
	if c, managed, err := managedConfig(); err == nil && managed && c.ServerURL != "" {
		return "straza enroll"
	}
	return "straza enroll --server " + server
}

// Enroll enrolls c, a person on the sign-in lane req.Lane names or an agent
// headless, and hands each progress note, the code or the browser link to
// emit. The caller emits the done or error event from the result.
func (s *credStore) Enroll(ctx context.Context, c Caller, req localapi.EnrollRequest, emit func(localapi.EnrollEvent)) (localapi.EnrollResult, error) {
	server, keys, err := enrollServer(req.ServerURL, emit)
	if err != nil {
		return localapi.EnrollResult{}, err
	}
	if req.Headless {
		return s.enrollAgent(ctx, c, server, keys, req.Username, emit)
	}
	return s.enrollPerson(ctx, c, server, keys, req.Lane, emit)
}

// enrollServer returns the server an enrollment uses and the snapshot keys
// a managed config pins. A managed config wins over the server the caller
// named, as it does for every other use.
func enrollServer(named string, emit func(localapi.EnrollEvent)) (string, map[string]string, error) {
	named = strings.TrimRight(named, "/")
	c, managed, err := managedConfig()
	if err != nil {
		return "", nil, err
	}
	if managed && c.ServerURL != "" {
		server := strings.TrimRight(c.ServerURL, "/")
		if named != "" && named != server {
			emit(localapi.EnrollEvent{Event: localapi.EnrollNote,
				Text: "This machine's managed configuration names the server " + server + ", so Straza enrolls there."})
		}
		return server, c.SnapshotKeys, nil
	}
	if named == "" {
		return "", nil, &Refused{Status: http.StatusBadRequest, Sentence: "Straza needs the address of your Straza server to enroll. " +
			"Run `straza enroll --server <url>` with the address strazad serves on, such as https://straza.example.com."}
	}
	if u, err := url.Parse(named); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", nil, &Refused{Status: http.StatusBadRequest, Sentence: fmt.Sprintf("Straza cannot enroll with the server %q, because it is not an http or https URL. "+
			"Run `straza enroll --server <url>` with the address strazad serves on, such as https://straza.example.com.", named)}
	}
	return named, nil, nil
}

// enrollPerson signs c in at the issuer the server names on lane, makes a
// new key, and enrolls it on the key lane with its custody, the machine
// binding and counter 0. The key comes before the sign-in, because the
// browser lane's nonce names it. Each enrollment makes a new key, because
// the server refuses a key it already holds. The ID token never leaves the
// service and is not kept.
func (s *credStore) enrollPerson(ctx context.Context, c Caller, server string, keys map[string]string, lane string, emit func(localapi.EnrollEvent)) (localapi.EnrollResult, error) {
	if lane != "" && lane != localapi.LaneBrowser && lane != localapi.LaneDeviceCode {
		return localapi.EnrollResult{}, &Refused{Status: http.StatusBadRequest, Sentence: fmt.Sprintf("Straza cannot sign you in on the lane %q, "+
			"because it knows browser and device-code only. Run `%[2]s`, or `%[2]s --use-device-code` to sign in with a code.", lane, enrollCommand(server))}
	}
	plain := agentguard.NewClient(server)
	flow, err := plain.DiscoverLogin(ctx)
	if err != nil {
		return localapi.EnrollResult{}, err
	}
	if flow.Issuer != server {
		emit(localapi.EnrollEvent{Event: localapi.EnrollNote, Text: "Signing in at your identity provider: " + flow.Issuer})
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return localapi.EnrollResult{}, err
	}
	thumb := dpop.Thumbprint(key.Public().(ed25519.PublicKey))
	idToken, err := personSignIn(ctx, plain, flow, lane, thumb, emit)
	if err != nil {
		return localapi.EnrollResult{}, err
	}
	if len(keys) == 0 {
		if keys, err = plain.SnapshotKeys(ctx); err != nil {
			return localapi.EnrollResult{}, fmt.Errorf("fetch the server's snapshot keys before enrolling: %w", err)
		}
	}
	bindingKey, binding, err := newBinding(s.machine)
	if err != nil {
		return localapi.EnrollResult{}, err
	}
	host, _ := os.Hostname()
	custody := custodyOf(s.selfUID, c.UID)
	a := s.account(c.UID)
	if err := a.keyLock.lock(ctx); err != nil {
		return localapi.EnrollResult{}, err
	}
	defer a.keyLock.unlock()
	pt := &proofTransport{base: s.base, key: key, origin: originOf(server), nonces: a.nonces}
	ans, err := postJSON(ctx, signedClient(server, pt), "/v1/enroll", enrollBody{
		IDToken: idToken, ClientKind: "kit",
		Device:  deviceBody{Name: host, Platform: runtime.GOOS, Fingerprint: "host:" + host},
		Custody: custodyBody{Kind: custody, Platform: runtime.GOOS + "/" + runtime.GOARCH, Version: version.Version},
		// A new key starts at counter 0, and each check-in sends a higher one.
		MachineBinding: binding, Counter: 0,
	})
	if err != nil {
		return localapi.EnrollResult{}, fmt.Errorf("the server at %s did not answer the enrollment: %w", server, err)
	}
	if ans.status != http.StatusOK {
		return localapi.EnrollResult{}, enrollRefusal(server, ans)
	}
	var got enrollAnswer
	if err := json.Unmarshal(ans.body, &got); err != nil {
		return localapi.EnrollResult{}, fmt.Errorf("the server's enrollment answer is not JSON: %w", err)
	}
	switch {
	case got.KeyThumbprint == "":
		return localapi.EnrollResult{}, &Refused{Status: http.StatusBadGateway, Sentence: "The server enrolled this machine without binding its key, " +
			"so it is older than this Straza client and made a device that works without one. Ask your Straza admin to update the server " +
			"and revoke that device, then run `" + enrollCommand(server) + "` again."}
	case got.KeyThumbprint != thumb:
		return localapi.EnrollResult{}, &Refused{Status: http.StatusBadGateway, Sentence: "The server bound a key other than the one this machine made, " +
			"so Straza did not keep the enrollment. Run `" + enrollCommand(server) + "` again, and tell your Straza admin if it happens again."}
	}
	rec := enrollment{
		ServerURL: server, Kind: localapi.KindPerson, Username: got.Username, UserID: got.UserID, DeviceID: got.DeviceID,
		KeyThumbprint: thumb, Custody: custody, SnapshotKeys: keys, BindingKey: bindingKey, Binding: binding,
		SignedInAt: s.now().UTC(), SignInExpiresIn: got.SignInExpiresIn,
	}
	if err := s.replace(a, rec, key); err != nil {
		return localapi.EnrollResult{}, fmt.Errorf("the server enrolled device %s, and this machine could not keep its key, "+
			"so revoke that device and run `%s` again: %w", got.DeviceID, enrollCommand(server), err)
	}
	s.log.Info("enrolled a person", "uid", c.UID, "user", got.Username, "device", got.DeviceID, "custody", custody)
	return localapi.EnrollResult{
		ServerURL: server, Kind: localapi.KindPerson, Username: got.Username, UserID: got.UserID, DeviceID: got.DeviceID,
		KeyThumbprint: thumb, Custody: custody, SignInExpiresIn: got.SignInExpiresIn,
	}, nil
}

// personSignIn signs the person in on lane and returns the ID token: the
// browser lane with a nonce that names the key thumb, or the device-code
// lane, which the empty lane also means. A server whose idp.json requires
// the browser lane refuses the code lane here, before the person sees a
// code.
func personSignIn(ctx context.Context, plain *agentguard.Client, flow oidcflow.Flow, lane, thumb string, emit func(localapi.EnrollEvent)) (string, error) {
	if lane == localapi.LaneBrowser {
		return oidcflow.SignIn(ctx, plain.HTTP, flow, oidcflow.Nonce(thumb), enrollCommand(plain.Base), func(link string) {
			emit(localapi.EnrollEvent{Event: localapi.EnrollBrowser, VerificationURI: link})
		})
	}
	if flow.MinSignIn == "browser" {
		return "", &Refused{Status: http.StatusForbidden, Sentence: codeLaneRefused(plain.Base)}
	}
	if err := flow.CheckDevice(); err != nil {
		return "", err
	}
	auth, err := plain.StartDeviceFlow(ctx, flow)
	if err != nil {
		return "", err
	}
	emit(localapi.EnrollEvent{Event: localapi.EnrollCode, UserCode: auth.UserCode, VerificationURI: auth.VerificationURI})
	return pollDeviceFlow(ctx, plain, flow, auth, emit)
}

// pollDeviceFlow polls the token endpoint until the person approves the
// code or it expires. A person watching a silent terminal cannot tell
// waiting from hung, so every eighth poll repeats the code to approve. A
// slow_down answer makes every later poll wait 5 seconds longer, as RFC
// 8628 section 3.5 says.
func pollDeviceFlow(ctx context.Context, c *agentguard.Client, flow oidcflow.Flow, auth agentguard.DeviceAuth, emit func(localapi.EnrollEvent)) (string, error) {
	interval := time.Duration(auth.Interval) * time.Second
	if interval <= 0 {
		interval = 2 * time.Second
	}
	deadline := time.Now().Add(time.Duration(auth.ExpiresIn) * time.Second)
	for polls := 0; time.Now().Before(deadline); {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		tok, wait, err := c.PollToken(ctx, flow, auth.DeviceCode)
		switch {
		case err != nil:
			return "", err
		case wait == "" && tok == "":
			return "", errors.New("the identity provider approved the sign-in but sent no ID token. " +
				"Ask IT to allow the openid scope for the Straza client, then run `" + enrollCommand(c.Base) + "` again")
		case wait == "":
			return tok, nil
		case wait == "slow_down":
			interval += 5 * time.Second
		}
		polls++
		if polls%8 == 0 {
			emit(localapi.EnrollEvent{Event: localapi.EnrollNote, Text: fmt.Sprintf(
				"Still waiting for you to approve code %s. It expires in %s.", auth.UserCode, time.Until(deadline).Round(time.Second))})
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(interval):
		}
	}
	return "", deviceCodeExpired(c.Base)
}

// enrollRefusal turns a refused enrollment into the sentence the person
// sees. The server's own refusals pass through unchanged.
func enrollRefusal(server string, ans answer) error {
	var refuse *agentguard.StatusError
	if !errors.As(ans.refusal("/v1/enroll"), &refuse) {
		return ans.refusal("/v1/enroll")
	}
	switch {
	case refuse.Msg == fmt.Sprintf("HTTP %d", refuse.Status):
		return &Refused{Status: http.StatusBadGateway, Sentence: fmt.Sprintf("The server answered this machine's enrollment with HTTP %d and gave no reason. "+
			"Check that %s reaches strazad with nothing in front of it that blocks Straza, and run `straza doctor`.", refuse.Status, server)}
	case refuse.Status >= http.StatusInternalServerError:
		return &Refused{Status: http.StatusBadGateway, Sentence: fmt.Sprintf("The server could not enroll this machine and answered HTTP %d: %s "+
			"Run `%s` again in a minute, and run `straza doctor` if it keeps failing.", refuse.Status, endSentence(refuse.Msg), enrollCommand(server))}
	}
	return &Refused{Status: refuse.Status, Sentence: refuse.Msg}
}
