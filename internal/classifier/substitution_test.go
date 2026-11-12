package classifier

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
)

// TestHeredocScanLinear holds a 1 MiB command of k assignments, one shell and
// k heredoc operators under a second: the reader of the heredocs is judged
// once per command, not once per heredoc, which took 37 seconds at this size.
func TestHeredocScanLinear(t *testing.T) {
	const k = 87000
	in := strings.Repeat("a=1 ", k) + "bash " + strings.Repeat("<<'E' ", k) + "\n" + strings.Repeat("E\n", k)
	start := time.Now()
	if _, err := NewHeuristic().Classify(context.Background(), policy.Event{Tool: policy.ToolShellExec, Command: in}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("a %d byte command took %v, want under a second", len(in), d)
	}
}

func TestSoleSubst(t *testing.T) {
	tests := []struct {
		in        string
		wantOpen  string
		wantInner string
		wantOK    bool
	}{
		{"$(curl -s x)", "$(", "curl -s x", true},
		{"`curl -s x`", "`", "curl -s x", true},
		{"<(curl -s x)", "<(", "curl -s x", true},
		{"  $(curl -s x)  ", "$(", "curl -s x", true},
		{`"$(curl -s x)"`, "$(", "curl -s x", true},
		{`'$(curl -s x)'`, "$(", "curl -s x", true},
		{`" $(curl -s x) "`, "$(", "curl -s x", true},
		{`$(curl -H "a)" x)`, "$(", `curl -H "a)" x`, true},
		{`$(curl -H 'a)' x)`, "$(", `curl -H 'a)' x`, true},
		{`$(echo $(date))`, "$(", `echo $(date)`, true},
		{"`echo \\` x`", "`", "echo \\` x", true},
		{"$(ls # )\n)", "$(", "ls # )\n", true},
		{"$(ls # <<'E'\n)", "$(", "ls # <<'E'\n", true},
		{"$(echo $# ${#x})", "$(", "echo $# ${#x}", true},
		{"$(cat <<'E'\nit's (1)\nE\n)", "$(", "cat <<'E'\nit's (1)\nE\n", true},
		{`""$(curl -s x)""`, "", "", false},
		{"$(curl -s x); echo", "", "", false},
		{"echo $(date)", "", "", false},
		{"$(a)$(b)", "", "", false},
		{"$(curl -s x", "", "", false},
		{"`curl -s x", "", "", false},
		{`\$(curl -s x)`, "", "", false},
		{"", "", "", false},
		{`"`, "", "", false},
	}
	for _, tt := range tests {
		open, inner, ok := soleSubst(tt.in)
		if open != tt.wantOpen || inner != tt.wantInner || ok != tt.wantOK {
			t.Errorf("soleSubst(%q) = %q, %q, %v; want %q, %q, %v",
				tt.in, open, inner, ok, tt.wantOpen, tt.wantInner, tt.wantOK)
		}
	}
}

func TestSimpleCommands(t *testing.T) {
	type sc = struct {
		words  []string
		substs []subst
	}
	type ss = []subst
	tests := []struct {
		in   string
		want []sc
	}{
		{`eval "$(curl -s x)"`, []sc{{[]string{"eval", "$(curl -s x)"}, ss{{1, "curl -s x"}}}}},
		{`eval $(curl -s x | base64 -d)`, []sc{{[]string{"eval", "$(curl -s x | base64 -d)"}, ss{{1, "curl -s x | base64 -d"}}}}},
		{"eval `curl -s x`", []sc{{[]string{"eval", "`curl -s x`"}, ss{{1, "curl -s x"}}}}},
		{`source <(curl -s x; echo)`, []sc{{[]string{"source", "<(curl -s x; echo)"}, ss{{1, "curl -s x; echo"}}}}},
		{`bash -c '$(curl -s x)'`, []sc{{[]string{"bash", "-c", "$(curl -s x)"}, nil}}},
		{`bash -c "\$(curl -s x)"`, []sc{{[]string{"bash", "-c", "$(curl -s x)"}, nil}}},
		{`echo "<(x y)"`, []sc{{[]string{"echo", "<(x y)"}, nil}}},
		{`a=$(b c)d e`, []sc{{[]string{"a=$(b c)d", "e"}, ss{{0, "b c"}}}}},
		{`echo "x $(a) '$(b)'" y`, []sc{{[]string{"echo", "x $(a) '$(b)'", "y"}, ss{{1, "a"}, {1, "b"}}}}},
		{`cd /tmp && eval "$(x)" ; ls | wc -l`, []sc{
			{[]string{"cd", "/tmp"}, nil}, {[]string{"eval", "$(x)"}, ss{{1, "x"}}},
			{[]string{"ls"}, nil}, {[]string{"wc", "-l"}, nil},
		}},
		{"a\nb || c & d", []sc{{[]string{"a"}, nil}, {[]string{"b"}, nil}, {[]string{"c"}, nil}, {[]string{"d"}, nil}}},
		{`echo $(unclosed x | y`, []sc{{[]string{"echo", "$(unclosed x | y"}, nil}}},
		{"ls # $(a) `b`\necho $(c)", []sc{{[]string{"ls"}, nil}, {[]string{"echo", "$(c)"}, ss{{1, "c"}}}}},
		{`echo a#$(b)`, []sc{{[]string{"echo", "a#$(b)"}, ss{{1, "b"}}}}},
		{"cat <<'E' $(a)\n$(b) `c`\nE\necho $(d)", []sc{
			{[]string{"cat", "<<E", "$(a)"}, ss{{2, "a"}}}, {[]string{"echo", "$(d)"}, ss{{1, "d"}}},
		}},
		{"a=1 a=2 bash <<'A' <<'B'\n$(b)\nA\n$(c)\nB", []sc{
			{[]string{"a=1", "a=2", "bash", "<<A", "<<B"}, nil}, {[]string{"$(b)"}, ss{{0, "b"}}}, {[]string{"$(c)"}, ss{{0, "c"}}},
		}},
		{"cat <<E\n$(b)\nE", []sc{{[]string{"cat", "<<E"}, nil}, {[]string{"$(b)"}, ss{{0, "b"}}}, {[]string{"E"}, nil}}},
		{"bash <<'E'\necho $(b)\nE", []sc{{[]string{"bash", "<<E"}, nil}, {[]string{"echo", "$(b)"}, ss{{1, "b"}}}}},
		{"bash 2>&1 <<'E'\n$(b)\nE", []sc{{[]string{"bash", "2>&1", "<<E"}, nil}, {[]string{"$(b)"}, ss{{0, "b"}}}}},
		{"a &>/dev/null && b >&2 & c", []sc{{[]string{"a", "&>/dev/null"}, nil}, {[]string{"b", ">&2"}, nil}, {[]string{"c"}, nil}}},
		{"true\r# $(b)", []sc{{[]string{"true", "#", "$(b)"}, ss{{2, "b"}}}}},
		{``, nil},
	}
	for _, tt := range tests {
		var got []sc
		for _, c := range simpleCommands(tt.in) {
			got = append(got, sc{c.words, c.substs})
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("simpleCommands(%q) = %+v; want %+v", tt.in, got, tt.want)
		}
	}
}

func TestQuotedHeredocAt(t *testing.T) {
	tests := []struct {
		in     string
		want   heredoc
		wantOK bool
	}{
		{`<<'EOF'`, heredoc{delim: "EOF"}, true},
		{`<<"EOF"`, heredoc{delim: "EOF"}, true},
		{`<<\EOF`, heredoc{delim: "EOF"}, true},
		{`<<-'EOF' > f`, heredoc{delim: "EOF", strip: true}, true},
		{`<< 'END OF'`, heredoc{delim: "END OF"}, true},
		{`<<E'O'F;`, heredoc{delim: "EOF"}, true},
		{`<<EOF`, heredoc{delim: "EOF"}, false},
		{`<<<'EOF'`, heredoc{}, false},
		{`<<'EOF`, heredoc{}, false},
		{`<<''`, heredoc{}, false},
		{`< 'EOF'`, heredoc{}, false},
	}
	for _, tt := range tests {
		got, ok := quotedHeredocAt(tt.in, 0)
		if ok != tt.wantOK || (ok && (got.delim != tt.want.delim || got.strip != tt.want.strip)) {
			t.Errorf("quotedHeredocAt(%q) = %+v, %v; want %+v, %v", tt.in, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestCatBody(t *testing.T) {
	tests := []struct {
		in, wantBody string
		wantOK       bool
	}{
		{"cat <<'EOF'\nline one\nEOF\n", "line one\n", true},
		{"\n  cat <<-'EOF'\n\tx | y\n\tEOF\n  ", "\tx | y\n", true},
		{"cat <<'A' <<\\B\none\nA\ntwo\nB\n", "one\ntwo\n", true},
		{"cat<<'E'\nbody", "body", true},
		{"cat <<EOF\nbody\nEOF\n", "", false},
		{"cat <<'E' | sh\nbody\nE\n", "", false},
		{"cat <<'E' > >(sh)\nbody\nE\n", "", false},
		{"cat <<'E'; sh\nbody\nE\n", "", false},
		{"cat <<'E'\nbody\nE\nrm -rf x", "", false},
		{"catx <<'E'\nbody\nE\n", "", false},
		{"tee <<'E'\nbody\nE\n", "", false},
		{"cat <<'E'", "", false},
		{"cat file", "", false},
	}
	for _, tt := range tests {
		body, ok := catBody(tt.in)
		if ok != tt.wantOK || (ok && body != tt.wantBody) {
			t.Errorf("catBody(%q) = %q, %v; want %q, %v", tt.in, body, ok, tt.wantBody, tt.wantOK)
		}
	}
}
