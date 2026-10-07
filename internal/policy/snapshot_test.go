package policy

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

func mustCompile(t *testing.T, localDefault string, docs ...string) Snapshot {
	t.Helper()
	raw := make([][]byte, len(docs))
	for i, d := range docs {
		raw[i] = []byte(d)
	}
	snap, err := Compile(CompileInput{Documents: raw, LocalDefault: localDefault, MaxAge: 900, CreatedUnix: 1700000000})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return snap
}

const denyShell = `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: %NAME% }
spec:
  rules:
    - id: block-rm
      tools: [shell.exec]
      command: { denyPatterns: ["rm -rf *"] }
      effect: deny
      reason: "no"
`

func named(name string) string {
	out := make([]byte, 0, len(denyShell))
	for i := 0; i < len(denyShell); i++ {
		if i+6 <= len(denyShell) && denyShell[i:i+6] == "%NAME%" {
			out = append(out, name...)
			i += 5
			continue
		}
		out = append(out, denyShell[i])
	}
	return string(out)
}

func TestSnapshotRoundTrip(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	lookup := func(kid string) (ed25519.PublicKey, bool) {
		if kid == "k1" {
			return pub, true
		}
		return nil, false
	}

	snap := mustCompile(t, EffectAllow, named("set-a"))
	signed, id, err := snap.Sign("k1", priv)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if id == "" {
		t.Fatal("empty snapshot id")
	}

	eng, decoded, err := OpenSnapshot(signed, id, lookup)
	if err != nil {
		t.Fatalf("OpenSnapshot: %v", err)
	}
	if decoded.MaxAgeSecs != 900 || decoded.LocalDefault != EffectAllow {
		t.Errorf("decoded snapshot fields: %+v", decoded)
	}
	// The rebuilt engine enforces the policy.
	d := eng.Evaluate(Event{Kind: EventToolPre, Tool: ToolShellExec, Command: "rm -rf /"}, Subject{User: "x"})
	if d.Effect != EffectDeny {
		t.Errorf("rebuilt engine did not enforce policy: %+v", d)
	}
}

func TestSnapshotDeterministicOrdering(t *testing.T) {
	// Same sets in different input order → identical signed bytes (content
	// addressing depends on this).
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_ = pub
	s1 := mustCompile(t, EffectAllow, named("bbb"), named("aaa"))
	s2 := mustCompile(t, EffectAllow, named("aaa"), named("bbb"))
	_, id1, _ := s1.Sign("k1", priv)
	_, id2, _ := s2.Sign("k1", priv)
	if id1 != id2 {
		t.Errorf("snapshot ids differ by input order: %s vs %s", id1, id2)
	}
}

func TestSnapshotTamperRejected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	lookup := func(string) (ed25519.PublicKey, bool) { return pub, true }

	snap := mustCompile(t, EffectAllow, named("set-a"))
	signed, id, _ := snap.Sign("k1", priv)

	// Flip a byte deep in the payload.
	tampered := make([]byte, len(signed))
	copy(tampered, signed)
	tampered[len(tampered)/2] ^= 0xFF
	if _, _, err := OpenSnapshot(tampered, "", lookup); err == nil {
		t.Fatal("tampered snapshot accepted")
	}

	// Correct bytes but wrong expected id.
	if _, _, err := OpenSnapshot(signed, id+"00", lookup); err == nil {
		t.Fatal("snapshot with mismatched id accepted")
	}
}

func TestSnapshotUnknownKeyRejected(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	snap := mustCompile(t, EffectAllow, named("set-a"))
	signed, id, _ := snap.Sign("k1", priv)

	empty := func(string) (ed25519.PublicKey, bool) { return nil, false }
	if _, _, err := OpenSnapshot(signed, id, empty); err == nil {
		t.Fatal("snapshot with unknown signing key accepted")
	}

	// A different key that is "known" but wrong must fail signature check.
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	wrong := func(string) (ed25519.PublicKey, bool) { return otherPub, true }
	if _, _, err := OpenSnapshot(signed, id, wrong); err == nil {
		t.Fatal("snapshot verified against the wrong public key")
	}
}

func TestCompileRejectsBadDocsAndDuplicates(t *testing.T) {
	if _, err := Compile(CompileInput{Documents: [][]byte{[]byte("not: a policyset")}, LocalDefault: EffectAllow}); err == nil {
		t.Error("compile accepted an invalid document")
	}
	if _, err := Compile(CompileInput{
		Documents:    [][]byte{[]byte(named("dup")), []byte(named("dup"))},
		LocalDefault: EffectAllow,
	}); err == nil {
		t.Error("compile accepted duplicate set names")
	}
	if _, err := Compile(CompileInput{Documents: nil, LocalDefault: "sometimes"}); err == nil {
		t.Error("compile accepted a bad localDefault")
	}
}
