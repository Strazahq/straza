package manager

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestEchoValues pins the values a reply is checked for: the credential as
// stored and as its template renders it when that differs, and none for a
// credential under eight bytes.
func TestEchoValues(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name, template, value string
		want                  []string
	}{
		{"bare template", SecretPlaceholder, "sk-12345678", []string{"sk-12345678"}},
		{"header template", "Bearer {{secret}}", "sk-12345678", []string{"sk-12345678", "Bearer sk-12345678"}},
		{"eight bytes", "Bearer {{secret}}", "12345678", []string{"12345678", "Bearer 12345678"}},
		{"seven bytes", "Bearer {{secret}}", "1234567", nil},
		{"a pre-encoded Basic credential", "Basic {{secret}}", "YWxpY2U6aHVudGVyMjIyMg==",
			[]string{"YWxpY2U6aHVudGVyMjIyMg==", "Basic YWxpY2U6aHVudGVyMjIyMg==", "alice:hunter2222"}},
		{"base64 that is not user and password", SecretPlaceholder, b64("sk-12345678"), []string{b64("sk-12345678")}},
		{"empty", SecretPlaceholder, "", nil},
	}
	for _, row := range rows {
		if got := echoValues(row.template, row.value); !slices.Equal(got, row.want) {
			t.Errorf("%s: echoValues = %q, want %q", row.name, got, row.want)
		}
	}
}

// TestEchoes pins which replies hold an injected credential: the whole
// value or a full standard encoding of it, in any text a reply carries,
// and in the bytes of a blob. Text that decodes, as percent escapes, base64
// or JSON, is read again decoded, so an upstream's own encoder hides
// nothing. A fragment, a value under eight bytes and a clean reply never
// match.
func TestEchoes(t *testing.T) {
	t.Parallel()
	const v = "sk-live?8827/NEVER+LEAK~>"
	b := []byte(v)
	header := "Bearer " + v
	values := []string{v, header}
	text := func(s string) *mcp.CallToolResult {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
	}
	jsonText := func(v any) string {
		out, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	review := func(v string) []string { return echoValues("Bearer {{secret}}", v) }
	rows := []struct {
		name   string
		values []string
		res    *mcp.CallToolResult
		err    error
		want   bool
	}{
		{"whole value", values, text(v), nil, true},
		{"value inside a sentence", values, text("your token is " + v + ", keep it"), nil, true},
		{"standard base64 padded", values, text(base64.StdEncoding.EncodeToString(b)), nil, true},
		{"standard base64 unpadded", values, text(base64.RawStdEncoding.EncodeToString(b)), nil, true},
		{"URL base64 padded", values, text(base64.URLEncoding.EncodeToString(b)), nil, true},
		{"URL base64 unpadded", values, text(base64.RawURLEncoding.EncodeToString(b)), nil, true},
		{"hex lower case", values, text(hex.EncodeToString(b)), nil, true},
		{"hex upper case", values, text(strings.ToUpper(hex.EncodeToString(b))), nil, true},
		{"query escaped", values, text(url.QueryEscape(v)), nil, true},
		{"path escaped", values, text(url.PathEscape(v)), nil, true},
		{"base64 of the header value inside a sentence", values,
			text("seen: " + base64.StdEncoding.EncodeToString([]byte(header)) + " end"), nil, true},
		{"structured content value", values,
			&mcp.CallToolResult{StructuredContent: map[string]any{"a": []any{map[string]any{"b": "x" + v}}}}, nil, true},
		{"structured content key", values,
			&mcp.CallToolResult{StructuredContent: map[string]any{v: 1}}, nil, true},
		{"structured content number", []string{"98765432109876"},
			&mcp.CallToolResult{StructuredContent: map[string]any{"n": 98765432109876}}, nil, true},
		{"result _meta", values, &mcp.CallToolResult{Meta: mcp.Meta{"debug": header}}, nil, true},
		{"image bytes at an offset", values,
			&mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{Data: []byte("xx" + v + "y"), MIMEType: "image/png"}}}, nil, true},
		{"embedded resource text", values,
			&mcp.CallToolResult{Content: []mcp.Content{&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file:///a", Text: v}}}}, nil, true},
		{"embedded resource blob", values,
			&mcp.CallToolResult{Content: []mcp.Content{&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file:///a", Blob: []byte("z" + v)}}}}, nil, true},
		{"resource link address", values,
			&mcp.CallToolResult{Content: []mcp.Content{&mcp.ResourceLink{URI: "https://x.test/?t=" + url.QueryEscape(v), Name: "n"}}}, nil, true},
		{"error text", values, nil, errors.New("upstream said 401 for " + header), true},
		{"error text in base64", values, nil, errors.New("echo " + base64.StdEncoding.EncodeToString(b)), true},
		{"tool error result", values, &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "bad token " + v}}}, nil, true},
		{"JSON text with the value's quote and backslash escaped", review(`db"Pa\ss-9921x`),
			text(jsonText(map[string]string{"password": `db"Pa\ss-9921x`})), nil, true},
		{"JSON text with Go's <, > and & escaping", review("tok<abc>&defgh"),
			text(jsonText(map[string]string{"token": "tok<abc>&defgh"})), nil, true},
		{"JSON text with / escaped", review("AbCd+EfGh/IjKl=="), text(`{"token":"AbCd+EfGh\/IjKl=="}`), nil, true},
		{"percent escaping that keeps /", review("AbCd+EfGh/IjKl=="), text("https://x/?t=AbCd%2BEfGh/IjKl%3D%3D"), nil, true},
		{"lower-case percent escapes", review("AbCd+EfGh/IjKl=="), text("https://x/?t=AbCd%2bEfGh%2fIjKl%3d%3d"), nil, true},
		{"URL base64 of a JSON document holding the header", review("sk-live-0123456789"),
			text(base64.RawURLEncoding.EncodeToString([]byte(`{"authorization":"Bearer sk-live-0123456789","x":1}`))), nil, true},
		{"unpadded base64 of a document holding the value", review("sk-live-0123456789"),
			text(base64.RawStdEncoding.EncodeToString([]byte(`{"a":"x sk-live-0123456789"}`))), nil, true},
		{"padded base64 of a document inside a sentence", review("sk-live-0123456789"),
			text("headers: " + base64.StdEncoding.EncodeToString([]byte(`{"a":"x sk-live-0123456789"}`)) + " end"), nil, true},
		{"the user and password of a pre-encoded Basic credential", echoValues("Basic {{secret}}", "YWxpY2U6aHVudGVyMjIyMg=="),
			text("login failed for alice:hunter2222"), nil, true},
		{"base64 inside JSON inside base64", review("sk-live-0123456789"),
			text(base64.StdEncoding.EncodeToString([]byte(jsonText(map[string]string{"blob": base64.StdEncoding.EncodeToString([]byte("k=sk-live-0123456789"))})))), nil, true},
		{"a value with a space form-encoded with +", review("my pass word 77"), text("u=" + url.QueryEscape("my pass word 77")), nil, true},
		{"JSON after a prefix with the value's quote escaped", review(`db"Pa\ss-9921x`),
			text("upstream error: " + jsonText(map[string]string{"password": `db"Pa\ss-9921x`})), nil, true},
		{"JSON after a prefix with \\u escapes", review("tok<abc>&defgh"), text(`upstream error: {"token":"tok\u003cabc\u003e\u0026defgh"}`), nil, true},
		{"standard base64 glued to a hyphenated word", review("sk-live-0123456789"),
			text("sha256-" + base64.StdEncoding.EncodeToString([]byte("x+/ sk-live-0123456789 y"))), nil, true},
		{"standard base64 holding + glued to a hyphenated word", review("sk-live-0123456789"),
			text("sha256-" + base64.StdEncoding.EncodeToString([]byte("\xfb\xff\xbf sk-live-0123456789 y"))), nil, true},
		{"fragment of the value", values, text(v[:len(v)-1]), nil, false},
		{"fragment form-encoded with +", review("my pass word 77"), text("u=" + url.QueryEscape("my pass word 7")), nil, false},
		{"fragment after a JSON prefix", review(`db"Pa\ss-9921x`),
			text("upstream error: " + jsonText(map[string]string{"password": `db"Pa\ss-9921`})), nil, false},
		{"fragment in base64 glued to a hyphenated word", review("sk-live-0123456789"),
			text("sha256-" + base64.StdEncoding.EncodeToString([]byte("x+/ sk-live-012345678 y"))), nil, false},
		{"fragment inside base64 of a document", review("sk-live-0123456789"),
			text(base64.StdEncoding.EncodeToString([]byte(`{"a":"x sk-live-012345678"}`))), nil, false},
		{"fragment inside JSON text", review(`db"Pa\ss-9921x`), text(jsonText(map[string]string{"password": `db"Pa\ss-9921`})), nil, false},
		{"fragment of its base64", values, text(base64.StdEncoding.EncodeToString(b)[:12]), nil, false},
		{"value under eight bytes", []string{"1234567"}, text("1234567"), nil, false},
		{"no values", nil, text(v), nil, false},
		{"clean reply", values, text("echo: hi"), errors.New("connection refused"), false},
		{"nothing", values, nil, nil, false},
	}
	for _, row := range rows {
		if got := echoes(row.values, row.res, row.err); got != row.want {
			t.Errorf("%s: echoes = %v, want %v", row.name, got, row.want)
		}
	}
}

// b64 is the standard base64 of s, so a fixture value is spelled as what it
// encodes rather than as an opaque literal.
func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
