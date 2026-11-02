package gitremote

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// pl returns payload as one pkt-line.
func pl(payload string) string { return string(appendPkt(nil, payload)) }

const (
	oidA  = "1111111111111111111111111111111111111111"
	oidB  = "2222222222222222222222222222222222222222"
	zeros = "0000000000000000000000000000000000000000"
)

func TestReadPkt(t *testing.T) {
	cases := []struct {
		name, in string
		kind     pktKind
		payload  string
		err      string
	}{
		{"data", pl("hello\n"), pktData, "hello\n", ""},
		{"empty data", "0004", pktData, "", ""},
		{"flush", "0000", pktFlush, "", ""},
		{"delim", "0001", pktDelim, "", ""},
		{"response end", "0002", pktEnd, "", ""},
		{"upper-case hex", "000Ahello\n", pktData, "hello\n", ""},
		{"length three", "0003", 0, "", "outside git's bounds"},
		{"over the bound", "fff1" + strings.Repeat("x", 65517), 0, "", "outside git's bounds"},
		{"not hex", "00zz", 0, "", "not four hex digits"},
		{"hex prefix", "0x10", 0, "", "not four hex digits"},
		{"short length", "00", 0, "", errTruncated.Error()},
		{"short payload", "0010abc", 0, "", errTruncated.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := readPkt(strings.NewReader(tc.in))
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil || p.kind != tc.kind || string(p.payload()) != tc.payload || string(p.raw) != tc.in {
				t.Fatalf("got %+v %q, %v", p, p.payload(), err)
			}
		})
	}
	if _, err := readPkt(strings.NewReader("")); !errors.Is(err, io.EOF) {
		t.Fatalf("empty input: %v, want io.EOF", err)
	}
	longest := "fff0" + strings.Repeat("x", 65516)
	if p, err := readPkt(strings.NewReader(longest)); err != nil || len(p.raw) != pktMax {
		t.Fatalf("the longest pkt-line: %d bytes, %v", len(p.raw), err)
	}
}

func TestUntilFlush(t *testing.T) {
	req := pl("command=fetch\n") + "0001" + pl("want "+oidA+"\n") + "0000"
	in := strings.NewReader(req + pl("command=ls-refs\n"))
	got, err := io.ReadAll(&untilFlush{r: in})
	if err != nil || string(got) != req {
		t.Fatalf("got %q, %v", got, err)
	}
	if rest, _ := io.ReadAll(in); string(rest) != pl("command=ls-refs\n") {
		t.Fatalf("read past the flush: %q left", rest)
	}
	u := &untilFlush{r: strings.NewReader(pl("command=fetch\n"))}
	if _, err := io.ReadAll(u); !errors.Is(err, errTruncated) || !errors.Is(u.err, errTruncated) {
		t.Fatalf("a request cut off before its flush: %v, kept %v", err, u.err)
	}
}

func TestReadPushHead(t *testing.T) {
	cmd1 := oidA + " " + oidB + " refs/heads/main"
	cmd2 := zeros + " " + oidB + " refs/heads/feature"
	del := oidA + " " + zeros + " refs/heads/old"
	caps := "\x00report-status side-band-64k quiet object-format=sha1 agent=git/2.47.3\n"
	cases := []struct {
		name, in   string
		refs       []localapi.GitRef
		caps       []string
		rawLen     int // bytes the head took, -1 for all of in
		deleteOnly bool
		err        string
	}{
		{name: "nothing to push", in: "0000", rawLen: -1},
		{name: "no input", in: "", rawLen: -1},
		{name: "two commands then the pack", in: pl(cmd1+caps) + pl(cmd2+"\n") + "0000" + "PACK...",
			refs:   []localapi.GitRef{{Ref: "refs/heads/main", Old: oidA, New: oidB}, {Ref: "refs/heads/feature", Old: zeros, New: oidB}},
			caps:   []string{"report-status", "side-band-64k", "quiet", "object-format=sha1", "agent=git/2.47.3"},
			rawLen: len(pl(cmd1+caps) + pl(cmd2+"\n") + "0000")},
		{name: "shallow lines first", in: pl("shallow "+oidA+"\n") + pl(cmd1+caps) + "0000",
			refs: []localapi.GitRef{{Ref: "refs/heads/main", Old: oidA, New: oidB}},
			caps: []string{"report-status", "side-band-64k", "quiet", "object-format=sha1", "agent=git/2.47.3"}, rawLen: -1},
		{name: "push options section", in: pl(cmd1+"\x00report-status push-options\n") + "0000" + pl("ci.skip\n") + "0000" + "PACK",
			refs: []localapi.GitRef{{Ref: "refs/heads/main", Old: oidA, New: oidB}}, caps: []string{"report-status", "push-options"},
			rawLen: len(pl(cmd1+"\x00report-status push-options\n") + "0000" + pl("ci.skip\n") + "0000")},
		{name: "delete only", in: pl(del+caps) + "0000", deleteOnly: true, rawLen: -1,
			refs: []localapi.GitRef{{Ref: "refs/heads/old", Old: oidA, New: zeros}},
			caps: []string{"report-status", "side-band-64k", "quiet", "object-format=sha1", "agent=git/2.47.3"}},
		{name: "sha256 ids", in: pl(strings.Repeat("a", 64)+" "+strings.Repeat("b", 64)+" refs/heads/main\n") + "0000", rawLen: -1,
			refs: []localapi.GitRef{{Ref: "refs/heads/main", Old: strings.Repeat("a", 64), New: strings.Repeat("b", 64)}}},
		{name: "upper-case id", in: pl(strings.Repeat("A", 40)+" "+oidB+" refs/heads/main\n") + "0000", err: "not an old id"},
		{name: "mixed id lengths", in: pl(oidA+" "+strings.Repeat("b", 64)+" refs/heads/main\n") + "0000", err: "not an old id"},
		{name: "signed push", in: pl("push-cert\x00report-status\n") + "0000", err: "not an old id"},
		{name: "two fields", in: pl(oidA+" refs/heads/main\n") + "0000", err: "not an old id"},
		{name: "shallow after a command", in: pl(cmd1+"\n") + pl("shallow "+oidA+"\n") + "0000", err: "not an old id"},
		{name: "bad shallow", in: pl("shallow nope\n") + "0000", err: "without an object id"},
		{name: "delim inside", in: pl(cmd1+"\n") + "0001", err: "special pkt-line"},
		{name: "ends before the flush", in: pl(cmd1 + "\n"), err: errTruncated.Error()},
		{name: "bad length", in: "zzzz", err: "not four hex digits"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, err := readPushHead(strings.NewReader(tc.in))
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := tc.rawLen
			if want < 0 {
				want = len(tc.in)
			}
			if string(h.raw) != tc.in[:want] {
				t.Fatalf("raw %q, want %q", h.raw, tc.in[:want])
			}
			if !refsEqual(h.refs, tc.refs) || strings.Join(h.caps, " ") != strings.Join(tc.caps, " ") || h.deleteOnly() != tc.deleteOnly {
				t.Fatalf("refs %+v caps %q deleteOnly %v", h.refs, h.caps, h.deleteOnly())
			}
		})
	}
}

func TestReadPushHeadBound(t *testing.T) {
	var b strings.Builder
	for i := 0; b.Len() <= headMax; i++ {
		b.WriteString(pl(oidA + " " + oidB + " refs/heads/" + strings.Repeat("x", 200) + "\n"))
	}
	b.WriteString("0000")
	if _, err := readPushHead(strings.NewReader(b.String())); !errors.Is(err, errHeadTooLarge) {
		t.Fatalf("a command section over 1 MiB: %v", err)
	}
}

func refsEqual(a, b []localapi.GitRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRefusalReport(t *testing.T) {
	refs := []localapi.GitRef{{Ref: "refs/heads/main", Old: oidA, New: oidB}, {Ref: "refs/heads/dev", Old: zeros, New: oidB}}
	sentence := "Straza refused this push: no role allows it.\nAsk an administrator."
	inner := pl("unpack ok\n") + pl("ng refs/heads/main Straza refused this push: no role allows it. Ask an administrator.\n") +
		pl("ng refs/heads/dev Straza refused this push: no role allows it. Ask an administrator.\n") + "0000"
	cases := []struct {
		name string
		caps []string
		want string
	}{
		{"no report asked", []string{"side-band-64k", "quiet"}, ""},
		{"plain report", []string{"report-status"}, inner},
		{"report v2", []string{"report-status-v2"}, inner},
		{"side-band-64k", []string{"report-status", "side-band-64k"}, pl("\x01"+inner) + "0000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := refusalReport(pushHead{refs: refs, caps: tc.caps}, sentence)
			if string(got) != tc.want {
				t.Fatalf("got %q\nwant %q", got, tc.want)
			}
		})
	}

	// The old side-band carries at most 1000 bytes per pkt-line, so a long
	// report spans several band 1 packets that join to the same bytes.
	long := strings.Repeat("why ", 300)
	got := refusalReport(pushHead{refs: refs, caps: []string{"report-status", "side-band"}}, long)
	r := bytes.NewReader(got)
	var joined []byte
	for {
		p, err := readPkt(r)
		if err != nil {
			t.Fatal(err)
		}
		if p.kind == pktFlush {
			break
		}
		if len(p.raw) > 1000 || p.payload()[0] != 1 {
			t.Fatalf("band packet of %d bytes on band %d", len(p.raw), p.payload()[0])
		}
		joined = append(joined, p.payload()[1:]...)
	}
	if r.Len() != 0 || !strings.HasPrefix(string(joined), pl("unpack ok\n")) || !strings.HasSuffix(string(joined), "0000") {
		t.Fatalf("side-band report does not frame the inner report: %q", joined)
	}
}

func TestOneLineAndClip(t *testing.T) {
	if got := oneLine(" a\n\tb\x00  c\x7f "); got != "a b c" {
		t.Fatalf("oneLine: %q", got)
	}
	if got := clipTo("aé", 2); got != "a" {
		t.Fatalf("clipTo split a rune: %q", got)
	}
}
