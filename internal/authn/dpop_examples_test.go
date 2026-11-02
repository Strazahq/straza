package authn

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// proofExample is one file of spec/session-token/examples.
type proofExample struct {
	Provenance  string `json:"provenance"`
	Description string `json:"description"`
	At          string `json:"at"`
	Request     struct {
		Method      string   `json:"method"`
		Path        string   `json:"path"`
		Origins     []string `json:"origins"`
		AccessToken string   `json:"access_token"`
		Thumbprint  string   `json:"thumbprint"`
		Counter     int64    `json:"counter"`
	} `json:"request"`
	Proof  string         `json:"proof"`
	Header map[string]any `json:"header"`
	Claims map[string]any `json:"claims"`
	Reason string         `json:"reason"`
}

// TestProofExamplesReplay replays every published example proof through
// ProofVerifier at the example's time, with the nonce key the examples were
// generated under (the bytes 0x00 to 0x1f). A valid-* example must pass and
// an invalid-* one must fail for its stated reason. Each example's decoded
// header and claims must equal what its compact proof carries, and the
// proof schema must agree with the file name, except that an example marked
// "INVALID (semantic)" breaks a rule the schema cannot express.
func TestProofExamplesReplay(t *testing.T) {
	dir := filepath.Join("..", "..", "spec", "session-token", "examples")
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	sch, err := jsonschema.NewCompiler().Compile(filepath.Join("..", "..", "spec", "session-token", "dpop-proof.schema.json"))
	if err != nil {
		t.Fatalf("compile the proof schema: %v", err)
	}

	var valid, invalid int
	for _, path := range files {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var ex proofExample
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

			parts := strings.Split(ex.Proof, ".")
			if len(parts) != 3 {
				t.Fatalf("proof has %d parts", len(parts))
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
					t.Errorf("proof segment %d decodes to %v, but the example shows %v", i, got, want)
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

			at, err := time.Parse(time.RFC3339, ex.At)
			if err != nil {
				t.Fatal(err)
			}
			check := ProofCheck{Method: ex.Request.Method, Path: ex.Request.Path, Origins: ex.Request.Origins,
				AccessToken: ex.Request.AccessToken, Thumbprint: ex.Request.Thumbprint, Counter: ex.Request.Counter}
			_, err = NewProofVerifier(testNonceKey(0)).Verify(ex.Proof, check, at)
			var pe *ProofError
			switch {
			case wantValid && err != nil:
				t.Fatalf("a valid example was refused: %v", err)
			case wantValid:
			case ex.Reason == "stale nonce":
				if !errors.Is(err, ErrStaleNonce) {
					t.Fatalf("err = %v, want ErrStaleNonce", err)
				}
			case !errors.As(err, &pe) || !strings.Contains(pe.Reason, ex.Reason):
				t.Fatalf("err = %v, want a refusal with reason %q", err, ex.Reason)
			}
		})
	}
	if valid < 3 || invalid < 3 {
		t.Fatalf("%d valid and %d invalid examples in %s, the spec asks for at least 3 of each", valid, invalid, dir)
	}
}
