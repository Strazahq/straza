package gitremote

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

var receiveAdvert = "001f# service=git-receive-pack\n0000" +
	pl(oidA+" refs/heads/main\x00report-status report-status-v2 delete-refs side-band-64k quiet atomic push-options object-format=sha1\n") + "0000"

func TestHelperPush(t *testing.T) {
	caps := "\x00report-status side-band-64k quiet object-format=sha1 agent=git/2.47.3\n"
	main := oidA + " " + oidB + " refs/heads/main"
	feature := zeros + " " + oidB + " refs/heads/feature"
	head := pl(main+caps) + pl(feature+"\n") + "0000"
	report := pl("\x01"+pl("unpack ok\n")+pl("ok refs/heads/main\n")+pl("ok refs/heads/feature\n")+"0000") + "0000"

	door := newDoor()
	door.adverts[receivePack] = receiveAdvert
	door.answers[receivePack] = report
	in := "capabilities\nconnect git-receive-pack\n" + head + "PACK" + strings.Repeat("x", 100<<10)
	got := runWith(t, door, nil, strings.NewReader(in))
	if got.code != 0 {
		t.Fatalf("got %+v", got)
	}
	if want := "connect\n\n" + "\n" + receiveAdvert[len("001f# service=git-receive-pack\n0000"):] + report; got.out != want {
		t.Fatalf("stdout %q\nwant %q", got.out, want)
	}
	if posted := door.posted[receivePack]; len(posted) != 1 || posted[0] != head+"PACK"+strings.Repeat("x", 100<<10) {
		t.Fatalf("the POST did not carry the commands followed by the pack (%d posts)", len(posted))
	}
	wantRefs := []localapi.GitRef{{Ref: "refs/heads/main", Old: oidA, New: oidB}, {Ref: "refs/heads/feature", Old: zeros, New: oidB}}
	if len(door.grants) != 2 || door.grants[0].Op != "push" || door.grants[0].Refs != nil ||
		door.grants[1].Op != "push" || !refsEqual(door.grants[1].Refs, wantRefs) {
		t.Fatalf("grants %+v, want one without refs, then one naming both updates", door.grants)
	}
}

func TestHelperPushNothing(t *testing.T) {
	door := newDoor()
	door.adverts[receivePack] = receiveAdvert
	got := runWith(t, door, nil, strings.NewReader("capabilities\nconnect git-receive-pack\n0000"))
	if got.code != 0 || len(door.posted[receivePack]) != 0 || len(door.grants) != 1 {
		t.Fatalf("got %+v, %d posts, %d grants: want a clean end after the advertisement", got, len(door.posted[receivePack]), len(door.grants))
	}
}

// TestHelperPushDeleteOnly feeds a stdin that never closes, as git does
// after commands that only delete, and wants the POST and the end at once.
func TestHelperPushDeleteOnly(t *testing.T) {
	door := newDoor()
	door.adverts[receivePack] = receiveAdvert
	door.answers[receivePack] = pl("unpack ok\n") + pl("ng refs/heads/old deletes are refused\n") + "0000"
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	head := pl(oidA+" "+zeros+" refs/heads/old\x00report-status delete-refs\n") + "0000"
	go func() { _, _ = io.WriteString(pw, "capabilities\nconnect git-receive-pack\n"+head) }()
	run := prepare(t, door, nil, pr)
	done := make(chan runResult, 1)
	go func() { done <- run() }()
	select {
	case got := <-done:
		if got.code != 0 || !strings.HasSuffix(got.out, door.answers[receivePack]) {
			t.Fatalf("got %+v", got)
		}
		if posted := door.posted[receivePack]; len(posted) != 1 || posted[0] != head {
			t.Fatalf("posted %q, want the commands alone", posted)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a delete-only push waited for stdin to close")
	}
}

func TestHelperPushRefused(t *testing.T) {
	sentence := "Straza refused this push: no role allows git_push on acme/pay-api. Ask an administrator for a role that allows git_push on acme/pay-api."
	refused := func(req localapi.GitGrantRequest) []localapi.GitGrantLine {
		if req.Refs == nil {
			return []localapi.GitGrantLine{{OK: true}}
		}
		return []localapi.GitGrantLine{{Error: sentence}}
	}
	main := oidA + " " + oidB + " refs/heads/main"
	cases := []struct {
		name, caps, pack string
		wantOut, wantErr string
		wantCode         int
	}{
		{"side-band report", "\x00report-status side-band-64k\n", "PACKBYTES",
			pl("\x01"+pl("unpack ok\n")+pl("ng refs/heads/main "+sentence+"\n")+"0000") + "0000", "", 0},
		{"plain report", "\x00report-status\n", "PACKBYTES", pl("unpack ok\n") + pl("ng refs/heads/main "+sentence+"\n") + "0000", "", 0},
		{"no report asked", "\x00quiet\n", "PACKBYTES", "", sentence, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			door := newDoor()
			door.adverts[receivePack] = receiveAdvert
			door.grantLines = refused
			stdin := strings.NewReader("capabilities\nconnect git-receive-pack\n" + pl(main+tc.caps) + "0000" + tc.pack)
			got := runWith(t, door, nil, stdin)
			prefix := "connect\n\n\n" + receiveAdvert[len("001f# service=git-receive-pack\n0000"):]
			if got.code != tc.wantCode || got.out != prefix+tc.wantOut || !strings.Contains(got.err, tc.wantErr) {
				t.Fatalf("got %+v", got)
			}
			if stdin.Len() != 0 {
				t.Fatalf("%d pack bytes left unread", stdin.Len())
			}
			if len(door.posted[receivePack]) != 0 {
				t.Fatal("a refused push reached the resource server")
			}
		})
	}
}

func TestHelperPushBadCommands(t *testing.T) {
	cases := []struct{ name, head, want string }{
		{"unreadable command", pl("push-cert\x00report-status\n") + "0000", "could not read the push commands"},
		{"too many commands", strings.Repeat(pl(oidA+" "+oidB+" refs/heads/"+strings.Repeat("x", 500)+"\n"), 2000) + "0000", sentHeadTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			door := newDoor()
			door.adverts[receivePack] = receiveAdvert
			got := runWith(t, door, nil, strings.NewReader("capabilities\nconnect git-receive-pack\n"+tc.head))
			if got.code != 1 || !strings.Contains(got.err, tc.want) || len(door.grants) != 1 {
				t.Fatalf("got %+v with %d grants", got, len(door.grants))
			}
		})
	}
}

func TestHelperPushAdvertisementRefused(t *testing.T) {
	door := newDoor()
	door.grantLines = func(localapi.GitGrantRequest) []localapi.GitGrantLine {
		return []localapi.GitGrantLine{{Error: "Straza refused this push: no role allows git_push on acme/pay-api."}}
	}
	got := runWith(t, door, nil, strings.NewReader("capabilities\nconnect git-receive-pack\n"))
	if got.code != 1 || got.out != "connect\n\n" || !strings.Contains(got.err, "no role allows git_push") {
		t.Fatalf("got %+v", got)
	}
}

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// TestHelperStreamsThePack pushes a 32 MiB pack and checks, when the first
// MiB of the POST reaches the resource side, that the helper has read at
// most half of it from git, so it streams and never buffers the pack. The
// bound leaves room for the several MiB loopback socket buffers take.
func TestHelperStreamsThePack(t *testing.T) {
	door := newDoor()
	door.adverts[receivePack] = receiveAdvert
	head := pl(oidA+" "+oidB+" refs/heads/main\x00report-status\n") + "0000"
	pack := bytes.Repeat([]byte("p"), 32<<20)
	stdin := &countingReader{r: io.MultiReader(strings.NewReader("capabilities\nconnect git-receive-pack\n"+head), bytes.NewReader(pack))}
	var readAtFirstMiB, posted int64
	streaming := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != localapi.RouteGit+receivePack {
			door.ServeHTTP(w, r)
			return
		}
		n, _ := io.CopyN(io.Discard, r.Body, 1<<20)
		readAtFirstMiB = stdin.n.Load()
		rest, _ := io.Copy(io.Discard, r.Body)
		posted = n + rest
	})
	got := runWith(t, streaming, nil, stdin)
	if got.code != 0 {
		t.Fatalf("got %+v", got)
	}
	if posted != int64(len(head)+len(pack)) {
		t.Fatalf("posted %d bytes, want %d", posted, len(head)+len(pack))
	}
	if readAtFirstMiB > 16<<20 {
		t.Fatalf("the helper had read %d bytes from git when the first MiB arrived, so it buffers the pack", readAtFirstMiB)
	}
}

// writeRecorder keeps each Write apart, so a test sees how the helper split
// what it sent git.
type writeRecorder struct{ writes []string }

func (w *writeRecorder) Write(p []byte) (int, error) {
	w.writes = append(w.writes, string(p))
	return len(p), nil
}

// TestHelperReadyLineIsAWriteOfItsOwn pins that the empty line answering
// connect reaches git in a write of its own, and the advertisement only in a
// later one. git reads that line through an unbuffered stream it then
// discards, and on Windows such a stream reads two bytes at a time, so a
// first pkt-line byte in the same write was lost and git stopped with "bad
// line length character".
func TestHelperReadyLineIsAWriteOfItsOwn(t *testing.T) {
	tests := []struct{ name, service, advert, in string }{
		{"fetch", uploadPack, "001e# service=git-upload-pack\n0000" + v2Advert, "capabilities\nconnect git-upload-pack\n0000"},
		{"push", receivePack, receiveAdvert, "capabilities\nconnect git-receive-pack\n0000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			door := newDoor()
			door.adverts[tt.service] = tt.advert
			srv := httptest.NewServer(door)
			t.Cleanup(srv.Close)
			var out writeRecorder
			h, err := newHelper([]string{"origin", testURL}, nil, strings.NewReader(tt.in), &out, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			h.base, h.http = srv.URL, srv.Client()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if code, handOff := h.run(ctx); code != 0 || handOff {
				t.Fatalf("run = %d, %v, want a clean end", code, handOff)
			}
			i := 0
			for i < len(out.writes) && out.writes[i] != "connect\n\n" {
				i++
			}
			if i+2 >= len(out.writes) || out.writes[i+1] != "\n" || !strings.HasPrefix(out.writes[i+2], "00") {
				t.Fatalf("writes to git %q, want the capabilities, then the empty line alone, then the advertisement", out.writes)
			}
		})
	}
}
