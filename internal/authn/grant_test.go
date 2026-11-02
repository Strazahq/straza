package authn

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jws"
)

const (
	testOld40 = "0123456789abcdef0123456789abcdef01234567"
	testNew40 = "89abcdef0123456789abcdef0123456789abcdef"
	testZero  = "0000000000000000000000000000000000000000"
	testNew64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// sampleGrant is a fetch grant MintGrant accepts.
func sampleGrant() Grant {
	return Grant{
		Audience: "https://ghapp.example",
		Subject:  "user-1",
		Session:  "ses-1",
		Harness:  "claude-code/2.1.0",
		Kind:     "human",
		JKT:      "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k",
		Server:   "ghapp",
		ObjType:  "repo",
		ObjValue: "acme/billing",
		ObjID:    "123456",
		Op:       "fetch",
		Decision: "evt-1",
	}
}

// headerOf decodes the protected header of a compact token without
// verifying it.
func headerOf(t *testing.T, raw string) map[string]any {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(strings.Split(raw, ".")[0])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestMintGrantClaims pins the header and the claim set of a minted grant,
// byte for byte after decoding, and checks that the session key set
// verifies its signature.
func TestMintGrantClaims(t *testing.T) {
	svc := testService(t, 0)
	kid, _ := svc.active.KeyID()
	push := sampleGrant()
	push.Op = "push"
	push.Refs = []GrantRef{
		{Ref: "refs/heads/main", Old: testOld40, New: testNew40},
		{Ref: "refs/heads/topic", Old: testZero, New: testNew40},
	}
	push.Approval = "apr-1"
	agent := sampleGrant()
	agent.Op = "push"
	agent.Harness = ""
	agent.Kind = "agent"

	cases := []struct {
		name  string
		in    Grant
		extra map[string]any // claims beyond the fetch claim set
		act   map[string]any
	}{
		{name: "a fetch grant carries no refs and no apr", in: sampleGrant(),
			act: map[string]any{"ses": "ses-1", "hrn": "claude-code/2.1.0", "kind": "human"}},
		{name: "a push grant with refs and an approval", in: push,
			extra: map[string]any{"op": "push", "apr": "apr-1", "refs": []any{
				map[string]any{"ref": "refs/heads/main", "old": testOld40, "new": testNew40},
				map[string]any{"ref": "refs/heads/topic", "old": testZero, "new": testNew40},
			}},
			act: map[string]any{"ses": "ses-1", "hrn": "claude-code/2.1.0", "kind": "human"}},
		{name: "a push grant without refs and without a harness", in: agent,
			extra: map[string]any{"op": "push"},
			act:   map[string]any{"ses": "ses-1", "kind": "agent"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, out, err := svc.MintGrant(tc.in)
			if err != nil {
				t.Fatalf("MintGrant: %v", err)
			}
			if _, err := uuid.Parse(out.JTI); err != nil {
				t.Errorf("JTI %q is not a UUID: %v", out.JTI, err)
			}
			if out.IssuedAt.IsZero() || out.Expiry.Sub(out.IssuedAt) != GrantTTL {
				t.Errorf("IssuedAt %v and Expiry %v are not %v apart", out.IssuedAt, out.Expiry, GrantTTL)
			}
			want := tc.in
			want.JTI, want.IssuedAt, want.Expiry = out.JTI, out.IssuedAt, out.Expiry
			if !reflect.DeepEqual(out, want) {
				t.Errorf("returned grant\n got %+v\nwant %+v", out, want)
			}

			wantHeader := map[string]any{"alg": "EdDSA", "kid": kid, "typ": GrantType}
			if got := headerOf(t, raw); !reflect.DeepEqual(got, wantHeader) {
				t.Errorf("header = %v, want %v", got, wantHeader)
			}
			wantClaims := map[string]any{
				"iss": "https://straza.local", "aud": "https://ghapp.example", "sub": "user-1",
				"act": tc.act, "iat": float64(out.IssuedAt.Unix()), "exp": float64(out.Expiry.Unix()),
				"jti": out.JTI, "cnf": map[string]any{"jkt": "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k"},
				"srv": "ghapp", "obj": map[string]any{"type": "repo", "value": "acme/billing", "id": "123456"},
				"op": "fetch", "dec": "evt-1",
			}
			for k, v := range tc.extra {
				wantClaims[k] = v
			}
			if got := payloadOf(t, raw); !reflect.DeepEqual(got, wantClaims) {
				t.Errorf("claims\n got %v\nwant %v", got, wantClaims)
			}

			svc.mu.RLock()
			set := svc.verify
			svc.mu.RUnlock()
			if _, err := jws.Verify([]byte(raw), jws.WithKeySet(set)); err != nil {
				t.Errorf("the session key set does not verify the grant: %v", err)
			}
		})
	}
}

// TestMintGrantRefuses pins every input MintGrant refuses before it signs.
func TestMintGrantRefuses(t *testing.T) {
	svc := testService(t, 0)
	manyRefs := make([]GrantRef, 1001)
	for i := range manyRefs {
		manyRefs[i] = GrantRef{Ref: fmt.Sprintf("refs/heads/b%d", i), Old: testZero, New: testNew40}
	}
	cases := []struct {
		name   string
		mutate func(*Grant)
		want   string
	}{
		{"empty audience", func(g *Grant) { g.Audience = "" }, "its audience is empty"},
		{"empty subject", func(g *Grant) { g.Subject = "" }, "its subject is empty"},
		{"empty session", func(g *Grant) { g.Session = "" }, "its session is empty"},
		{"empty kind", func(g *Grant) { g.Kind = "" }, "its kind is empty"},
		{"empty key thumbprint", func(g *Grant) { g.JKT = "" }, "its key thumbprint is empty"},
		{"empty server", func(g *Grant) { g.Server = "" }, "its server is empty"},
		{"empty object type", func(g *Grant) { g.ObjType = "" }, "its object type is empty"},
		{"empty object value", func(g *Grant) { g.ObjValue = "" }, "its object value is empty"},
		{"empty object id", func(g *Grant) { g.ObjID = "" }, "its object id is empty"},
		{"empty decision", func(g *Grant) { g.Decision = "" }, "its decision id is empty"},
		{"unknown kind", func(g *Grant) { g.Kind = "robot" }, `kind "robot" is not human, agent or service`},
		{"empty op", func(g *Grant) { g.Op = "" }, `op "" is neither fetch nor push`},
		{"advertise op", func(g *Grant) { g.Op = "advertise" }, `op "advertise" is neither fetch nor push`},
		{"refs on a fetch", func(g *Grant) {
			g.Refs = []GrantRef{{Ref: "refs/heads/main", Old: testOld40, New: testNew40}}
		}, "a fetch grant names no refs"},
		{"more than 1000 refs", func(g *Grant) { g.Op, g.Refs = "push", manyRefs }, "it names 1001 refs, and the limit is 1000"},
		{"a short ref name", func(g *Grant) {
			g.Op, g.Refs = "push", []GrantRef{{Ref: "heads/main", Old: testOld40, New: testNew40}}
		}, `ref "heads/main" is not a full ref name under refs/`},
		{"a bare refs/ prefix", func(g *Grant) {
			g.Op, g.Refs = "push", []GrantRef{{Ref: "refs/", Old: testOld40, New: testNew40}}
		}, `ref "refs/" is not a full ref name under refs/`},
		{"an upper-case old id", func(g *Grant) {
			g.Op, g.Refs = "push", []GrantRef{{Ref: "refs/heads/main", Old: strings.ToUpper(testOld40), New: testNew40}}
		}, `the old id of "refs/heads/main" is not lower-case hex of 40 or 64 characters`},
		{"a new id of 39 characters", func(g *Grant) {
			g.Op, g.Refs = "push", []GrantRef{{Ref: "refs/heads/main", Old: testOld40, New: testNew40[:39]}}
		}, `the new id of "refs/heads/main" is not lower-case hex of 40 or 64 characters`},
		{"a new id that is not hex", func(g *Grant) {
			g.Op, g.Refs = "push", []GrantRef{{Ref: "refs/heads/main", Old: testOld40, New: "g" + testNew40[1:]}}
		}, `the new id of "refs/heads/main" is not lower-case hex of 40 or 64 characters`},
		{"old and new of different lengths", func(g *Grant) {
			g.Op, g.Refs = "push", []GrantRef{{Ref: "refs/heads/main", Old: testOld40, New: testNew64}}
		}, `the old and new ids of "refs/heads/main" differ in length`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := sampleGrant()
			tc.mutate(&g)
			raw, out, err := svc.MintGrant(g)
			if err == nil {
				t.Fatalf("MintGrant accepted the grant and returned %q", raw)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to say %q", err, tc.want)
			}
			if raw != "" || !reflect.DeepEqual(out, Grant{}) {
				t.Errorf("a refusal returned %q and %+v, want nothing", raw, out)
			}
		})
	}
}

// TestMintGrantAcceptsEdges pins inputs at the edge of the rules that
// MintGrant still signs. TestMintGrantSizeCap pins the most refs it signs.
func TestMintGrantAcceptsEdges(t *testing.T) {
	svc := testService(t, 0)
	cases := []struct {
		name   string
		mutate func(*Grant)
	}{
		{"sha256 ids", func(g *Grant) {
			g.Op, g.Refs = "push", []GrantRef{{Ref: "refs/heads/main", Old: strings.Repeat("0", 64), New: testNew64}}
		}},
		{"a delete names zeros as new", func(g *Grant) {
			g.Op, g.Refs = "push", []GrantRef{{Ref: "refs/heads/old", Old: testOld40, New: testZero}}
		}},
		{"a service account", func(g *Grant) { g.Kind = "service" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := sampleGrant()
			tc.mutate(&g)
			if _, _, err := svc.MintGrant(g); err != nil {
				t.Fatalf("MintGrant: %v", err)
			}
		})
	}
}

// pushWithRefs is a push grant that names n ref updates whose names all
// have the same length, so each ref adds the same number of bytes.
func pushWithRefs(n int) Grant {
	g := sampleGrant()
	g.Op = "push"
	for i := range n {
		g.Refs = append(g.Refs, GrantRef{Ref: fmt.Sprintf("refs/heads/b%03d", i), Old: testOld40, New: testNew40})
	}
	return g
}

// TestMintGrantSizeCap pins the byte cap at its edge. It finds the fewest
// refs whose signed grant is longer than GrantMaxBytes, with a jti, iat and
// exp as long as MintGrant's, then checks that MintGrant signs one ref
// fewer and refuses that count with ErrGrantTooLarge, well before the
// 1000-ref cap. The cap itself must keep the whole Authorization header
// line, CRLF included, under the 8 KiB line buffer of a default proxy.
func TestMintGrantSizeCap(t *testing.T) {
	if line := len("Authorization: DPoP ") + GrantMaxBytes + len("\r\n"); line >= 8192 {
		t.Fatalf("a grant of GrantMaxBytes makes a %d-byte Authorization header line, and a proxy with an 8 KiB line buffer refuses it", line)
	}
	svc := testService(t, 0)
	signedLen := func(n int) int {
		g := pushWithRefs(n)
		g.JTI = uuid.NewString()
		g.IssuedAt = time.Now().UTC().Truncate(time.Second)
		g.Expiry = g.IssuedAt.Add(GrantTTL)
		raw, err := signGrant(svc.active, svc.issuer, g)
		if err != nil {
			t.Fatal(err)
		}
		return len(raw)
	}
	over := 1
	for signedLen(over) <= GrantMaxBytes {
		over++
	}
	if over >= maxGrantRefs {
		t.Fatalf("%d refs fit in %d bytes, so the size cap never binds before the ref cap", over-1, GrantMaxBytes)
	}
	t.Logf("%d refs fit in %d bytes, and %d do not", over-1, GrantMaxBytes, over)

	raw, _, err := svc.MintGrant(pushWithRefs(over - 1))
	if err != nil {
		t.Fatalf("MintGrant refused %d refs: %v", over-1, err)
	}
	if len(raw) > GrantMaxBytes {
		t.Fatalf("MintGrant signed %d bytes, over the cap of %d", len(raw), GrantMaxBytes)
	}
	raw, out, err := svc.MintGrant(pushWithRefs(over))
	if !errors.Is(err, ErrGrantTooLarge) {
		t.Fatalf("MintGrant with %d refs: err = %v, want ErrGrantTooLarge", over, err)
	}
	if raw != "" || !reflect.DeepEqual(out, Grant{}) {
		t.Errorf("a refusal returned %q and %+v, want nothing", raw, out)
	}
}
