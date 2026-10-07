package authn

import (
	"context"
	"testing"
	"time"
)

// FuzzTokenVerify hammers every token verifier with mutated inputs seeded by
// real tokens of each kind. Tokens arrive as bearer strings from the network
// on every request. Anything but a valid token of the exact expected purpose
// must be rejected with an error: never a panic, and never a cross-purpose
// acceptance. A device token must not verify as a session token and so on,
// and the seeds make the fuzzer explore exactly that boundary.
func FuzzTokenVerify(f *testing.F) {
	svc, err := NewTokenService(context.Background(), testRepo(f), "https://straza.local", 0)
	if err != nil {
		f.Fatal(err)
	}
	session, _, err := svc.Mint(Claims{Subject: "u1", Session: "s1", Device: "d1"})
	if err != nil {
		f.Fatal(err)
	}
	id, err := svc.MintIDToken("u1", "straza", time.Hour, "kim", "kim@x.io")
	if err != nil {
		f.Fatal(err)
	}
	device, err := svc.MintDeviceToken("u1", "d1", time.Hour)
	if err != nil {
		f.Fatal(err)
	}
	connect, err := svc.MintConnectState("u1", "app1")
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range []string{session, id, device, connect, "", "a.b.c", "Bearer x"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if c, err := svc.Verify(raw); err == nil && c.Session != "" && raw != session {
			t.Errorf("non-session input verified as session token: %q", raw)
		}
		if _, err := svc.VerifyIDToken(raw, "straza"); err == nil && raw != id {
			t.Errorf("non-ID input verified as ID token: %q", raw)
		}
		if _, err := svc.VerifyDeviceToken(raw); err == nil && raw != device {
			t.Errorf("non-device input verified as device token: %q", raw)
		}
		if _, err := svc.VerifyConnectState(raw); err == nil && raw != connect {
			t.Errorf("non-connect input verified as connect state: %q", raw)
		}
	})
}
