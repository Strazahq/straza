package agentguard

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Enroll runs the OIDC device-code flow at the issuer strazad advertises
// (the external IdP in enterprise, the built-in issuer in standalone),
// registers this device, pins the snapshot verification keys, and persists
// identity + config. Progress goes to w.
func Enroll(ctx context.Context, store *Store, serverURL string, w io.Writer) error {
	client := NewClient(serverURL)

	flow, err := client.DiscoverLogin(ctx)
	if err != nil {
		return err
	}
	if flow.Issuer != strings.TrimSuffix(serverURL, "/") {
		fmt.Fprintf(w, "Signing in at your identity provider: %s\n", flow.Issuer)
	}
	auth, err := client.StartDeviceFlow(ctx, flow)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "To authorize this device, open:\n  %s\nand confirm code %s\n", auth.VerificationURI, auth.UserCode)

	interval := time.Duration(auth.Interval) * time.Second
	if interval <= 0 {
		interval = 2 * time.Second
	}
	deadline := time.Now().Add(time.Duration(auth.ExpiresIn) * time.Second)
	fmt.Fprintln(w, "waiting for approval in the browser…")
	var idToken string
	for polls := 0; time.Now().Before(deadline); {
		// Poll first: the user has usually already approved in the browser, so
		// a wait before the first poll is pure latency they eat every run. The
		// wait belongs between polls (loop bottom). A cancelled context must
		// fail closed here without an HTTP call, returning a clean ctx.Err()
		// rather than a wrapped transport error.
		if err := ctx.Err(); err != nil {
			return err
		}
		tok, pending, err := client.PollToken(ctx, flow, auth.DeviceCode)
		if err != nil {
			return err
		}
		if !pending {
			idToken = tok
			break
		}
		polls++
		// A human staring at a silent terminal cannot tell "waiting" from
		// "hung", so say so and repeat the code they must approve, because
		// the most common failure is approving a stale code from an old
		// browser tab.
		if polls%8 == 0 {
			fmt.Fprintf(w, "still waiting: approve code %s (this run's code; old tabs show stale ones). Expires in %s.\n",
				auth.UserCode, time.Until(deadline).Round(time.Second))
		}
		// Wait between polls (never before the first); cancellation during the
		// wait still returns ctx.Err().
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
	if idToken == "" {
		return fmt.Errorf("the device code expired before it was approved. Run enroll again and approve the NEWLY printed code (a browser tab from an earlier attempt shows a stale one)")
	}

	host, _ := os.Hostname()
	enrolled, err := client.Enroll(ctx, idToken, host, hostPlatform(), deviceFingerprint(host))
	if err != nil {
		return err
	}

	keys, err := client.SnapshotKeys(ctx)
	if err != nil {
		return fmt.Errorf("fetch snapshot keys: %w", err)
	}
	if err := store.SaveConfig(Config{ServerURL: serverURL, SnapshotKeys: keys}); err != nil {
		return err
	}
	if err := store.SaveIdentity(Identity{
		IDToken: idToken, DeviceID: enrolled.DeviceID, Username: enrolled.Username,
		DeviceToken: enrolled.DeviceToken,
	}); err != nil {
		return err
	}
	fmt.Fprintf(w, "Enrolled as %s (device %s).\n", enrolled.Username, enrolled.DeviceID)
	return nil
}

func hostPlatform() string {
	return osName()
}

// deviceFingerprint is a stable per-host id for user-mode enroll. A real
// device keypair or certificate is the managed-mode second factor.
func deviceFingerprint(host string) string {
	return "host:" + host
}
