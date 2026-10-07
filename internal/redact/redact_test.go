package redact

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestRedact pins the shared battery: every conservative credential shape is
// masked, and benign text is left alone.
func TestRedact(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantGone string
		wantKept string
	}{
		{"aws key", "creds: AKIAIOSFODNN7EXAMPLE ok", "AKIAIOSFODNN7EXAMPLE", "creds:"},
		{"github pat", "use ghp_abcdefghijklmnopqrstuvwxyz0123456789 here", "ghp_abcdef", "use"},
		{"bearer header", "Authorization: Bearer eyJhbGciOiJFUzI1NiJ9.payload.sig", "eyJhbGci", "Authorization:"},
		{"pem block", "key:\n-----BEGIN PRIVATE KEY-----\nMIIEvg==\n-----END PRIVATE KEY-----\ndone", "MIIEvg", "done"},
		{"straza api token", "token wat_0123456789abcdefghijklmnopqrstuvwxyzABCDEF used", "wat_0123", "used"},
		{"benign", "just a normal command line", "", "normal command"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Redact(tc.in)
			if tc.wantGone != "" && strings.Contains(got, tc.wantGone) {
				t.Errorf("secret survived redaction: %q still in %q", tc.wantGone, got)
			}
			if tc.wantKept != "" && !strings.Contains(got, tc.wantKept) {
				t.Errorf("benign text dropped: %q gone from %q", tc.wantKept, got)
			}
			if tc.wantGone != "" && !strings.Contains(got, Mark) {
				t.Errorf("%s: no redaction marker in %q", tc.name, got)
			}
		})
	}
}

// TestNeutralize pins the display-safety pass (source is pure ASCII: \u escapes
// build the raw runes; wantSub is the literal visible escape text).
func TestNeutralize(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantSub string
		wantRaw string
	}{
		{"ansi color", "ls\x1b[31mred\x1b[0m", "\\u001B", "\x1b"},
		{"rlo reorder", "open cod\u202etxt.exe", "\\u202E", "\u202e"},
		{"zero-width in ident", "trans\u200bfer", "\\u200B", "\u200b"},
		{"lone cr", "line1\rline2", "\\u000D", "\r"},
		{"c1 nel", "x\u0085y", "\\u0085", "\u0085"},
		{"bidi isolate", "a\u2066b\u2069c", "\\u2066", "\u2066"},
		{"bom", "\ufeffhead", "\\uFEFF", "\ufeff"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Neutralize(tc.in)
			if tc.wantSub != "" && !strings.Contains(got, tc.wantSub) {
				t.Errorf("missing escape %q in %q", tc.wantSub, got)
			}
			if tc.wantRaw != "" && strings.Contains(got, tc.wantRaw) {
				t.Errorf("raw offending char survived: %q", got)
			}
			if !utf8.ValidString(got) {
				t.Errorf("output not valid UTF-8: %q", got)
			}
		})
	}
	if got := Neutralize("a\nb\tc"); got != "a\nb\tc" {
		t.Errorf("newline/tab were altered: %q", got)
	}
	if got := Neutralize("x\xffy"); !strings.Contains(got, "\\xFF") || strings.Contains(got, "\xff") {
		t.Errorf("invalid byte not escaped: %q", got)
	}
}

func TestPreviewUnderCap(t *testing.T) {
	in := "the AWS key is AKIAIOSFODNN7EXAMPLE"
	got, truncated, n := Preview(in, PreviewMaxBytes)
	if truncated {
		t.Error("small input must not be truncated")
	}
	if strings.Contains(got, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("secret not redacted: %q", got)
	}
	if n != len(got) {
		t.Errorf("bytes = %d, want redacted length %d", n, len(got))
	}
}

func TestPreviewEmpty(t *testing.T) {
	got, truncated, n := Preview("", PreviewMaxBytes)
	if got != "" || truncated || n != 0 {
		t.Errorf("Preview empty = (%q, %v, %d), want (empty, false, 0)", got, truncated, n)
	}
}

func TestPreviewCapBoundary(t *testing.T) {
	max := 64
	atCap := strings.Repeat("a", max)
	got, truncated, n := Preview(atCap, max)
	if truncated || got != atCap || n != max {
		t.Errorf("at-cap: got (%q trunc=%v n=%d), want verbatim untruncated n=%d", got, truncated, n, max)
	}
	over := strings.Repeat("a", max+1)
	got, truncated, n = Preview(over, max)
	if !truncated {
		t.Error("one-over-cap must be truncated")
	}
	if len(got) > max {
		t.Errorf("truncated preview is %d bytes, over cap %d", len(got), max)
	}
	if n != max+1 {
		t.Errorf("bytes = %d, want pre-truncation length %d", n, max+1)
	}
	if !strings.Contains(got, "bytes elided") {
		t.Errorf("missing elision marker: %q", got)
	}
}

func TestPreviewSecretSpansBoundary(t *testing.T) {
	max := 128
	secret := "AKIAIOSFODNN7EXAMPLE"
	in := strings.Repeat("a", 90) + secret + strings.Repeat("b", 200)
	got, truncated, _ := Preview(in, max)
	if !truncated {
		t.Fatal("expected truncation for an over-cap input")
	}
	if strings.Contains(got, secret) {
		t.Errorf("whole secret survived: %q", got)
	}
	if strings.Contains(got, "AKIAIOSF") {
		t.Errorf("secret fragment survived the cut: %q", got)
	}
	if len(got) > max {
		t.Errorf("preview %d bytes over cap %d", len(got), max)
	}
}

func TestPreviewNeutralizesBeforeMeasuring(t *testing.T) {
	in := "rm \u202egpj.txt"
	got, _, n := Preview(in, PreviewMaxBytes)
	if strings.Contains(got, "\u202e") {
		t.Errorf("bidi override survived into preview: %q", got)
	}
	if !strings.Contains(got, "\\u202E") {
		t.Errorf("bidi override not escaped: %q", got)
	}
	if n != len(got) {
		t.Errorf("bytes = %d, want neutralized length %d", n, len(got))
	}
}

func TestPreviewCutMidEscape(t *testing.T) {
	max := 80
	in := strings.Repeat("\u202e", 200)
	got, truncated, _ := Preview(in, max)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if !utf8.ValidString(got) {
		t.Errorf("cut mid-escape produced invalid UTF-8: %q", got)
	}
	if strings.Contains(got, "\u202e") {
		t.Errorf("raw bidi char re-exposed: %q", got)
	}
	if len(got) > max {
		t.Errorf("preview %d bytes over cap %d", len(got), max)
	}
}

func TestPreviewMultibyteSafe(t *testing.T) {
	in := strings.Repeat("\u00e9", 2000)
	got, truncated, _ := Preview(in, PreviewMaxBytes)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if !utf8.ValidString(got) {
		t.Errorf("preview is not valid UTF-8: %q", got)
	}
	if strings.ContainsRune(got, 0xFFFD) {
		t.Error("preview contains a UTF-8 replacement char (split rune)")
	}
	if len(got) > PreviewMaxBytes {
		t.Errorf("preview %d bytes over cap %d", len(got), PreviewMaxBytes)
	}
}
