package trace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Record is one parsed trace line: the common fields lifted out, every other
// attribute (v included) kept in Attrs.
type Record struct {
	TS    time.Time
	Level string
	Msg   string
	Attrs map[string]any
}

// Summary describes the trace file for status and doctor.
type Summary struct {
	// Records is the number of parsable lines across both generations.
	Records int
	// Unreadable counts lines that did not parse (torn or hand-mangled).
	Unreadable int
	// Last is the newest parsable record, nil when there is none.
	Last *Record
	// Size is the on-disk size of the live file plus the rotated generation.
	Size int64
}

// readLines returns every non-empty line, oldest first: the rotated
// generation, then the live file. Bounded by construction (rotation caps what
// can ever be read here); an absent generation contributes nothing.
func readLines(stateDir string) []string {
	live := filepath.Join(stateDir, FileName)
	var out []string
	for _, path := range []string{live + ".1", live} {
		raw, err := os.ReadFile(path) // #nosec G304 -- our own state file under the straza home
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.TrimSpace(line) != "" {
				out = append(out, line)
			}
		}
	}
	return out
}

// parse decodes one line; ok is false when it is not a JSON object.
func parse(line string) (rec Record, ok bool) {
	dec := json.NewDecoder(strings.NewReader(line))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return Record{}, false
	}
	rec.Attrs = make(map[string]any, len(raw))
	for k, v := range raw {
		switch k {
		case "ts":
			if s, isStr := v.(string); isStr {
				if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
					rec.TS = t.UTC()
				}
			}
		case "level":
			rec.Level, _ = v.(string)
		case "msg":
			rec.Msg, _ = v.(string)
		default:
			rec.Attrs[k] = v
		}
	}
	return rec, true
}

// Tail returns the last n records (n <= 0 means all), oldest first, plus the
// count of unreadable lines among the tail it looked at.
func Tail(stateDir string, n int) (recs []Record, unreadable int) {
	lines := readLines(stateDir)
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for _, line := range lines {
		rec, ok := parse(line)
		if !ok {
			unreadable++
			continue
		}
		recs = append(recs, rec)
	}
	return recs, unreadable
}

// Summarize counts the records, finds the newest journal record (the newest
// INFO line: a decision or a call, which is what a human wants named; the
// newest record of any level only when no journal record exists), and sizes
// both files.
func Summarize(stateDir string) Summary {
	live := filepath.Join(stateDir, FileName)
	var s Summary
	for _, p := range []string{live + ".1", live} {
		if fi, err := os.Stat(p); err == nil {
			s.Size += fi.Size()
		}
	}
	var lastAny, lastJournal *Record
	for _, line := range readLines(stateDir) {
		rec, ok := parse(line)
		if !ok {
			s.Unreadable++
			continue
		}
		s.Records++
		last := rec
		lastAny = &last
		if rec.Level == "INFO" {
			lastJournal = &last
		}
	}
	s.Last = lastJournal
	if s.Last == nil {
		s.Last = lastAny
	}
	return s
}

// Render prints one record as the single greppable line `straza trace show`
// emits: RFC3339 UTC timestamp, level, kind, then the attributes as k=v with
// keys sorted; values are quoted only when they contain spaces, quotes, or
// are empty. Missing common fields render as "-".
func Render(r Record) string {
	var b strings.Builder
	if r.TS.IsZero() {
		b.WriteString("-")
	} else {
		b.WriteString(r.TS.UTC().Format(time.RFC3339))
	}
	b.WriteString(" " + dash(r.Level) + " " + dash(r.Msg))
	keys := make([]string, 0, len(r.Attrs))
	for k := range r.Attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(" " + k + "=" + renderValue(r.Attrs[k]))
	}
	return b.String()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func renderValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		if x == "" || strings.ContainsAny(x, " \t\"") {
			return strconv.Quote(x)
		}
		return x
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		raw, err := json.Marshal(x)
		if err != nil {
			return "?"
		}
		return string(raw)
	}
}
