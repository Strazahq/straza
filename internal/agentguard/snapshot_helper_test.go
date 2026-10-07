package agentguard

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
)

// testSignedPolicy compiles and signs one PolicySet the way the server does,
// returning the signed bytes, content-addressed id, and the base64 pinned
// key: everything a client Store needs to verify it.
func testSignedPolicy(t *testing.T, priv ed25519.PrivateKey, doc string) (signed []byte, id string) {
	t.Helper()
	snap, err := policy.Compile(policy.CompileInput{
		Documents: [][]byte{[]byte(doc)}, LocalDefault: "allow", MaxAge: 900,
		CreatedUnix: time.Now().Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	signed, id, err = snap.Sign("k1", priv)
	if err != nil {
		t.Fatal(err)
	}
	return signed, id
}

// testSnapshotKey generates the signing pair and its pinned-config encoding.
func testSnapshotKey(t *testing.T) (ed25519.PrivateKey, map[string]string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return priv, map[string]string{"k1": base64.StdEncoding.EncodeToString(pub)}
}
