package authn

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// grantExample is one file of spec/grant/examples.
type grantExample struct {
	Provenance  string `json:"provenance"`
	Description string `json:"description"`
	At          string `json:"at"`
	Verifier    struct {
		Issuer   string `json:"issuer"`
		Audience string `json:"audience"`
	} `json:"verifier"`
	Key    map[string]any `json:"key"`
	Grant  string         `json:"grant"`
	Header map[string]any `json:"header"`
	Claims map[string]any `json:"claims"`
	Reason string         `json:"reason,omitempty"`
}

func grantExampleDir() string { return filepath.Join("..", "..", "spec", "grant", "examples") }

func compileGrantSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	sch, err := jsonschema.NewCompiler().Compile(filepath.Join("..", "..", "spec", "grant", "grant.schema.json"))
	if err != nil {
		t.Fatalf("compile the grant schema: %v", err)
	}
	return sch
}

// TestGrantExamplesReplay checks every published grant example. Its decoded
// header and claims must equal what its compact grant carries, and the
// schema must agree with the file name, except that an example marked
// "INVALID (semantic)" breaks a rule the schema cannot express. A valid-*
// example must pass every offline rule of the verification order in
// spec/grant/SPEC.md, and an invalid-* one must break exactly the rule its
// reason names first.
func TestGrantExamplesReplay(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(grantExampleDir(), "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	sch := compileGrantSchema(t)
	var valid, invalid int
	for _, path := range files {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var ex grantExample
			if err := json.Unmarshal(raw, &ex); err != nil {
				t.Fatalf("decode: %v", err)
			}
			wantValid := strings.HasPrefix(name, "valid-")
			switch {
			case wantValid:
				valid++
			case strings.HasPrefix(name, "invalid-"):
				invalid++
				if ex.Reason == "" {
					t.Fatal("an invalid example states no reason")
				}
			default:
				t.Fatal("the file name starts with neither valid- nor invalid-")
			}
			if !strings.HasPrefix(ex.Provenance, "doc ") && !strings.HasPrefix(ex.Provenance, "live ") {
				t.Errorf("provenance %q does not say doc or live", ex.Provenance)
			}

			parts := strings.Split(ex.Grant, ".")
			if len(parts) != 3 {
				t.Fatalf("grant has %d parts", len(parts))
			}
			for i, want := range []map[string]any{ex.Header, ex.Claims} {
				b, err := base64.RawURLEncoding.DecodeString(parts[i])
				if err != nil {
					t.Fatal(err)
				}
				var got map[string]any
				if err := json.Unmarshal(b, &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("grant segment %d decodes to %v, but the example shows %v", i, got, want)
				}
			}

			doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			schemaOK := sch.Validate(doc) == nil
			if want := wantValid || strings.Contains(ex.Description, "INVALID (semantic)"); schemaOK != want {
				t.Errorf("schema verdict %v, want %v", schemaOK, want)
			}

			got := breakingRule(t, ex)
			switch {
			case wantValid && got != "":
				t.Fatalf("a valid example breaks a rule: %s", got)
			case !wantValid && got != ex.Reason:
				t.Fatalf("the first rule the example breaks is %q, but its reason says %q", got, ex.Reason)
			}
		})
	}
	if valid < 3 || invalid < 3 {
		t.Fatalf("%d valid and %d invalid examples, the spec asks for at least 3 of each", valid, invalid)
	}
}

// breakingRule runs the offline rules of the verification order in
// spec/grant/SPEC.md against ex and returns the reason of the first rule
// it breaks, or "" when it breaks none. The proof, route, path, refs
// against commands and spent-jti rules need a request, so they are not
// here.
func breakingRule(t *testing.T, ex grantExample) string {
	t.Helper()
	if len(ex.Grant) > GrantMaxBytes {
		return "the grant is longer than 8000 bytes"
	}
	keyJSON, err := json.Marshal(ex.Key)
	if err != nil {
		t.Fatal(err)
	}
	key, err := jwk.ParseKey(keyJSON)
	if err != nil {
		t.Fatalf("the example key does not parse: %v", err)
	}
	if kid, _ := key.KeyID(); kid == "" || ex.Header["kid"] != kid {
		return "unknown kid"
	}
	if ex.Header["typ"] != GrantType {
		return "typ is not straza-grant+jwt"
	}
	if ex.Header["alg"] != "EdDSA" {
		return "alg is not EdDSA"
	}
	if _, err := jws.Verify([]byte(ex.Grant), jws.WithKey(jwa.EdDSA(), key)); err != nil {
		return "signature does not verify"
	}
	for _, c := range []string{"iss", "aud", "sub", "act", "iat", "exp", "jti", "cnf", "srv", "obj", "op", "dec"} {
		if _, ok := ex.Claims[c]; !ok {
			return "claim " + c + " is missing"
		}
	}
	if cnf, _ := ex.Claims["cnf"].(map[string]any); cnf["jkt"] == nil || cnf["jkt"] == "" {
		return "cnf carries no jkt"
	}
	op := ex.Claims["op"]
	if op != "fetch" && op != "push" {
		return "op is neither fetch nor push"
	}
	if _, ok := ex.Claims["refs"]; ok && op == "fetch" {
		return "refs on a fetch"
	}
	if ex.Claims["iss"] != ex.Verifier.Issuer {
		return "iss is not the configured issuer"
	}
	if ex.Claims["aud"] != ex.Verifier.Audience {
		return "aud is not this server's git base URL"
	}
	at, err := time.Parse(time.RFC3339, ex.At)
	if err != nil {
		t.Fatal(err)
	}
	iat, _ := ex.Claims["iat"].(float64)
	exp, _ := ex.Claims["exp"].(float64)
	now := float64(at.Unix())
	switch {
	case exp <= iat:
		return "exp is not after iat"
	case exp-iat > 300:
		return "exp minus iat is over 300 seconds"
	case iat > now+60:
		return "iat is more than 60 seconds in the future"
	case now >= exp+60:
		return "the grant expired"
	}
	return ""
}

// TestMintGrantMatchesSchema checks that what MintGrant signs validates
// against spec/grant/grant.schema.json.
func TestMintGrantMatchesSchema(t *testing.T) {
	svc := testService(t, 0)
	sch := compileGrantSchema(t)
	push := sampleGrant()
	push.Op, push.Approval = "push", "apr-1"
	push.Refs = []GrantRef{{Ref: "refs/heads/main", Old: testOld40, New: testNew40}}
	bare := sampleGrant()
	bare.Op, bare.Harness, bare.Kind = "push", "", "agent"
	for name, g := range map[string]Grant{"fetch": sampleGrant(), "push with refs": push, "push without refs": bare} {
		t.Run(name, func(t *testing.T) {
			raw, _, err := svc.MintGrant(g)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := json.Marshal(map[string]any{"header": headerOf(t, raw), "claims": payloadOf(t, raw)})
			if err != nil {
				t.Fatal(err)
			}
			v, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
			if err != nil {
				t.Fatal(err)
			}
			if err := sch.Validate(v); err != nil {
				t.Fatalf("a minted grant fails the schema: %v", err)
			}
		})
	}
}
