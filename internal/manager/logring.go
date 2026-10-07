package manager

import (
	"strings"
	"sync"
	"time"
)

// Entry is one runtime log line with the time the ring received it, which
// is the only clock the console can trust across runtimes that stamp
// nothing, stamp local time, or stamp in their own format.
type Entry struct {
	At   time.Time
	Line string
}

// Ring is a bounded in-memory line buffer for app runtime output (stdout and
// stderr, served by `strazactl apps logs`). It is also an io.Writer that
// splits raw stream chunks into lines.
type Ring struct {
	mu    sync.Mutex
	lines []string
	times []time.Time
	next  int
	full  bool
	part  strings.Builder // trailing partial line from Write
}

// NewRing returns a ring holding at most capacity lines.
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = 256
	}
	return &Ring{lines: make([]string, capacity), times: make([]time.Time, capacity)}
}

// Append adds one complete line.
func (r *Ring) Append(line string) {
	r.mu.Lock()
	r.append(line)
	r.mu.Unlock()
}

// append keeps line as shown writes it, so no reader of the ring meets an
// address's user information or query, or a credential shape redact knows.
func (r *Ring) append(line string) {
	r.lines[r.next] = shown(line)
	r.times[r.next] = time.Now()
	r.next = (r.next + 1) % len(r.lines)
	if r.next == 0 {
		r.full = true
	}
}

// Write implements io.Writer for process stderr/stdout streams: complete
// lines are appended; a trailing partial line is buffered until its newline
// arrives.
func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.part.Write(p)
	for {
		s := r.part.String()
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			break
		}
		r.append(strings.TrimRight(s[:i], "\r"))
		r.part.Reset()
		r.part.WriteString(s[i+1:])
	}
	return len(p), nil
}

// Last returns up to n lines, oldest first.
func (r *Ring) Last(n int) []string {
	entries := r.Entries(n)
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Line
	}
	return out
}

// Entries returns up to n entries, oldest first, each with its receive time.
func (r *Ring) Entries(n int) []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	size := r.next
	if r.full {
		size = len(r.lines)
	}
	if n <= 0 || n > size {
		n = size
	}
	out := make([]Entry, 0, n)
	start := r.next - n
	if start < 0 {
		start += len(r.lines)
	}
	for i := 0; i < n; i++ {
		j := (start + i) % len(r.lines)
		out = append(out, Entry{At: r.times[j], Line: r.lines[j]})
	}
	return out
}
