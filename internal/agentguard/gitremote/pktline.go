package gitremote

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// Bounds of what the helper reads from git and from the service.
const (
	pktMax  = 65520   // the longest pkt-line git writes, length prefix included
	headMax = 1 << 20 // the push command section, push options included
	textMax = 64 << 10
)

// pktKind tells a data pkt-line from git's three special ones.
type pktKind int

const (
	pktData  pktKind = iota
	pktFlush         // 0000
	pktDelim         // 0001
	pktEnd           // 0002, the response end of protocol v2
)

// pkt is one pkt-line as it was read, its length prefix included.
type pkt struct {
	raw  []byte
	kind pktKind
}

// payload returns the bytes after the length prefix of a data pkt-line.
func (p pkt) payload() []byte {
	if p.kind != pktData {
		return nil
	}
	return p.raw[4:]
}

// errTruncated marks input that ended inside a pkt-line.
var errTruncated = errors.New("the input ended inside a pkt-line")

// readPkt reads one pkt-line from r. It returns io.EOF only when r ends
// before the first byte, and refuses a length outside git's bounds.
func readPkt(r io.Reader) (pkt, error) {
	var hdr [4]byte
	if n, err := io.ReadFull(r, hdr[:]); err != nil {
		if n == 0 && errors.Is(err, io.EOF) {
			return pkt{}, io.EOF
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return pkt{}, errTruncated
		}
		return pkt{}, err
	}
	n, err := strconv.ParseUint(string(hdr[:]), 16, 32)
	if err != nil {
		return pkt{}, fmt.Errorf("the pkt-line length %q is not four hex digits", hdr[:])
	}
	switch {
	case n == 0:
		return pkt{raw: hdr[:], kind: pktFlush}, nil
	case n == 1:
		return pkt{raw: hdr[:], kind: pktDelim}, nil
	case n == 2:
		return pkt{raw: hdr[:], kind: pktEnd}, nil
	case n < 4 || n > pktMax:
		return pkt{}, fmt.Errorf("the pkt-line length %d is outside git's bounds of 4 to %d", n, pktMax)
	}
	raw := make([]byte, n)
	copy(raw, hdr[:])
	if _, err := io.ReadFull(r, raw[4:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return pkt{}, errTruncated
		}
		return pkt{}, err
	}
	return pkt{raw: raw, kind: pktData}, nil
}

// appendPkt appends payload to b as one data pkt-line.
func appendPkt(b []byte, payload string) []byte {
	return append(fmt.Appendf(b, "%04x", len(payload)+4), payload...)
}

// untilFlush yields the pkt-lines it reads from r up to and including the
// next flush, then io.EOF. It is the body of one protocol v2 request. Err
// keeps the reason a read failed, because the HTTP client hides it.
type untilFlush struct {
	r    io.Reader
	buf  []byte
	done bool
	err  error
}

func (u *untilFlush) Read(p []byte) (int, error) {
	for len(u.buf) == 0 {
		if u.done {
			return 0, io.EOF
		}
		pk, err := readPkt(u.r)
		if errors.Is(err, io.EOF) {
			err = errTruncated
		}
		if err != nil {
			u.err = err
			return 0, err
		}
		u.buf, u.done = pk.raw, pk.kind == pktFlush
	}
	n := copy(p, u.buf)
	u.buf = u.buf[n:]
	return n, nil
}

// errHeadTooLarge marks a push command section over headMax.
var errHeadTooLarge = errors.New("the push commands are larger than git-remote-straza reads")

// pushHead is what git sends receive-pack before the pack: the shallow
// lines and commands, their flush, and the push options with their flush
// when the commands asked for them. Raw holds every byte read, in order.
type pushHead struct {
	raw  []byte
	refs []localapi.GitRef
	caps []string
}

// deleteOnly reports whether every command deletes a ref. git sends no pack
// after such commands and keeps the helper's stdin open.
func (h pushHead) deleteOnly() bool {
	return len(h.refs) > 0 && !slices.ContainsFunc(h.refs, func(r localapi.GitRef) bool {
		return strings.Trim(r.New, "0") != ""
	})
}

// readPushHead reads the push command section from r. Input that ends
// before the first byte, or a lone flush, is a push with nothing to send.
func readPushHead(r io.Reader) (pushHead, error) {
	var h pushHead
	options := false
	for {
		p, err := readPkt(r)
		if errors.Is(err, io.EOF) && len(h.raw) == 0 {
			return h, nil
		}
		if errors.Is(err, io.EOF) {
			err = errTruncated
		}
		if err != nil {
			return h, err
		}
		if len(h.raw)+len(p.raw) > headMax {
			return h, errHeadTooLarge
		}
		h.raw = append(h.raw, p.raw...)
		switch {
		case p.kind == pktFlush && !options && len(h.refs) > 0 && slices.Contains(h.caps, "push-options"):
			options = true
		case p.kind == pktFlush:
			return h, nil
		case p.kind != pktData:
			return h, errors.New("git sent a special pkt-line inside the push commands")
		case !options:
			if err := h.add(string(p.payload())); err != nil {
				return h, err
			}
		}
	}
}

// add parses one line of the command section: a shallow line before the
// commands, or `<old> <new> <ref>` with the capabilities after a NUL on
// the first command.
func (h *pushHead) add(line string) error {
	line = strings.TrimSuffix(line, "\n")
	if oid, ok := strings.CutPrefix(line, "shallow "); ok && len(h.refs) == 0 {
		if !isOID(oid) {
			return fmt.Errorf("git sent a shallow line %q without an object id", clip(line))
		}
		return nil
	}
	if len(h.refs) == 0 {
		if cmd, caps, ok := strings.Cut(line, "\x00"); ok {
			line, h.caps = cmd, strings.Fields(caps)
		}
	}
	f := strings.Split(line, " ")
	if len(f) != 3 || !isOID(f[0]) || !isOID(f[1]) || len(f[0]) != len(f[1]) || f[2] == "" {
		return fmt.Errorf("git sent a push command %q that is not an old id, a new id and a ref", clip(line))
	}
	h.refs = append(h.refs, localapi.GitRef{Ref: f[2], Old: f[0], New: f[1]})
	return nil
}

// isOID reports whether s is a full object id in lower-case hex: 40
// characters for SHA-1 and 64 for SHA-256.
func isOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	return strings.Trim(s, "0123456789abcdef") == ""
}

// refusalReport is the receive-pack report that refuses every ref of head
// with sentence: `unpack ok`, one `ng <ref> <sentence>` line per ref and a
// flush, in side-band framing when the commands asked for it. It is nil
// when the commands asked for no report.
func refusalReport(head pushHead, sentence string) []byte {
	if !slices.Contains(head.caps, "report-status") && !slices.Contains(head.caps, "report-status-v2") {
		return nil
	}
	reason := oneLine(sentence)
	inner := appendPkt(nil, "unpack ok\n")
	for _, r := range head.refs {
		room := max(0, pktMax-4-len("ng  \n")-len(r.Ref))
		inner = appendPkt(inner, "ng "+r.Ref+" "+clipTo(reason, room)+"\n")
	}
	inner = append(inner, "0000"...)
	band := 0
	switch {
	case slices.Contains(head.caps, "side-band-64k"):
		band = pktMax - 5
	case slices.Contains(head.caps, "side-band"):
		band = 1000 - 5
	default:
		return inner
	}
	var out bytes.Buffer
	for b := inner; len(b) > 0; {
		n := min(len(b), band)
		out.Write(appendPkt(nil, "\x01"+string(b[:n])))
		b = b[n:]
	}
	out.WriteString("0000")
	return out.Bytes()
}

// oneLine turns text into one printable line: control characters become
// spaces and runs of spaces shrink to one.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// clip shortens s for a sentence that quotes it.
func clip(s string) string { return clipTo(s, 120) }

// clipTo cuts s to at most n bytes without splitting a UTF-8 sequence.
func clipTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xc0 == 0x80 {
		n--
	}
	return s[:n]
}
