package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
)

// lenientDecoders names every function of this package that may read a
// request body with a decoder that tolerates unknown fields, and why.
// TestRequestBodiesDecodeStrictly refuses any other.
var lenientDecoders = map[string]string{
	"handleEnroll":       clientPlane,
	"handleCheckin":      clientPlane,
	"handleDecide":       clientPlane,
	"handleGrantResolve": clientPlane,
	"handleGrant":        clientPlane,
	"handleAuditBatch":   clientPlane,

	"handleConnectStart":  connectPlane,
	"handleConnectAgents": connectPlane,
	"handleConnectFinish": connectPlane,

	"handleApproverEnroll":           approverPlane,
	"handleApproverRefreshChallenge": approverPlane,
	"handleApproverRefresh":          approverPlane,
	"handleApproverPushPut":          approverPlane,
	"handleApproverPushDelete":       approverPlane,
	"handleApproverDecide":           approverPlane,
	"handleSelfEnrollToken":          approverPlane,

	"handleApproverEnrollToken": "answers 403 whatever the body holds, and reads it only to name a user in the refusal's record",
	"handleMCPPost":             "the MCP gateway reads JSON-RPC from any MCP client, and the protocol lets a message carry members a receiver does not know",
}

// The reasons a plane outside the admin API stays lenient.
const (
	clientPlane   = "the straza client on a governed machine can be newer than this strazad, which must still read its request"
	connectPlane  = "the connect routes serve the self-service page and the straza client, which can be newer than this strazad"
	approverPlane = "the approver app on a phone updates on its own schedule, so this strazad must read a newer build's request"
)

// TestRefusedUnknownField pins the words of the 400 and that every other
// decode error is left to the route.
func TestRefusedUnknownField(t *testing.T) {
	t.Parallel()
	type body struct {
		ExpiresIn int64 `json:"expires_in"`
	}
	for _, tc := range []struct {
		name, in string
		answered bool
		says     string
	}{
		{name: "known field", in: `{"expires_in": 60}`},
		{name: "field matched without regard to case", in: `{"Expires_In": 60}`},
		{name: "malformed body is the route's to word", in: `{"expires_in": `},
		{name: "wrong type is the route's to word", in: `{"expires_in": "soon"}`},
		{
			name: "misspelled field", in: `{"expiresIn": 60}`, answered: true,
			says: `{"error":"The request carries a field named \"expiresIn\", which this request does not take, so nothing was changed. ` +
				`Check its spelling against the API reference at https://docs.straza.ai/reference/api/ and send the request again."}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var v body
			w := httptest.NewRecorder()
			got := refusedUnknownField(w, decodeAdmin(strings.NewReader(tc.in), &v))
			if got != tc.answered {
				t.Fatalf("answered = %v, want %v", got, tc.answered)
			}
			if !tc.answered {
				if w.Body.Len() != 0 {
					t.Errorf("wrote %q for an error the route words", w.Body.String())
				}
				return
			}
			if w.Code != http.StatusBadRequest || strings.TrimSpace(w.Body.String()) != tc.says {
				t.Errorf("answer = %d %s, want 400 %s", w.Code, w.Body.String(), tc.says)
			}
		})
	}
}

// TestRequestBodiesDecodeStrictly is the gate behind the rule that an admin
// route refuses a field it does not know. A function that reads r.Body with
// encoding/json fails it unless lenientDecoders names it, and a function
// that calls decodeAdmin fails it unless it also calls refusedUnknownField,
// so the unknown field is named and never worded as a missing one.
func TestRequestBodiesDecodeStrictly(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	lenient := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			uses := bodyUses(fn)
			switch {
			case uses["r.Body"] && (uses["json.NewDecoder"] || uses["json.Unmarshal"]):
				lenient[fn.Name.Name] = true
				if lenientDecoders[fn.Name.Name] == "" {
					t.Errorf("%s: %s reads r.Body with encoding/json, which ignores a field it does not know. "+
						"An admin route decodes with decodeAdmin and refusedUnknownField. "+
						"A route that must read a newer client goes into lenientDecoders with the reason.",
						fset.Position(fn.Pos()), fn.Name.Name)
				}
			case uses["decodeAdmin"] && !uses["refusedUnknownField"] && fn.Name.Name != "decodeAdmin":
				t.Errorf("%s: %s calls decodeAdmin and never refusedUnknownField, so an unknown field would be answered in the words of another error.",
					fset.Position(fn.Pos()), fn.Name.Name)
			}
		}
	}
	var stale []string
	for name := range lenientDecoders {
		if !lenient[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	for _, name := range stale {
		t.Errorf("lenientDecoders names %s, which no longer reads r.Body with encoding/json. Remove the entry.", name)
	}
}

// bodyUses reports which of the selectors and calls the gate looks for
// appear anywhere in fn, function literals included.
func bodyUses(fn *ast.FuncDecl) map[string]bool {
	uses := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok {
				uses[id.Name+"."+x.Sel.Name] = true
			}
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok {
				uses[id.Name] = true
			}
		}
		return true
	})
	return uses
}
