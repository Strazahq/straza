package ctl

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeSessionJWT builds a parse-only token whose exp is far enough out that
// Do() skips the refresh path (signature checking is the server's job).
func fakeSessionJWT(t *testing.T) string {
	t.Helper()
	claims, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	b64 := base64.RawURLEncoding.EncodeToString
	return b64([]byte(`{"alg":"EdDSA"}`)) + "." + b64(claims) + "." + b64([]byte("sig"))
}

func loggedInClient(t *testing.T, base string) *Client {
	t.Helper()
	c := NewClient(base)
	c.CredsPath = filepath.Join(t.TempDir(), "credentials.json")
	raw, _ := json.Marshal(credentials{Server: base, SessionToken: fakeSessionJWT(t), SessionID: "ses-1"})
	if err := os.WriteFile(c.CredsPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return c
}
