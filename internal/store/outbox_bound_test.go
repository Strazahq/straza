package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// recordOf is the CloudEvent the bound tests start from: the envelope a
// reference producer writes, with data, marshalled as json.Marshal does.
func recordOf(t *testing.T, data any) string {
	t.Helper()
	ce, err := json.Marshal(map[string]any{
		"specversion": "1.0", "id": "0198a3c1-52e0-7d41-9b6a-3c4d5e6f7a81", "type": "straza.audit.tool",
		"source": "strazad", "time": "2026-10-31T09:14:02.118Z", "data": data,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(ce)
}

// cutWant is the text a cut string of v ends with.
func cutWant(v string) string {
	sum := sha256.Sum256([]byte(v))
	return fmt.Sprintf("... (%d bytes, sha256 %s)", len(v), hex.EncodeToString(sum[:]))
}

// decodeBound decodes a bounded record, keeping numbers as written.
func decodeBound(t *testing.T, ce string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(ce))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("bounded record is not JSON: %v", err)
	}
	return m
}

// checkCut asserts that got is a prefix of whole on a character boundary
// followed by the cut text of whole.
func checkCut(t *testing.T, name, got, whole string) {
	t.Helper()
	prefix, ok := strings.CutSuffix(got, cutWant(whole))
	if !ok {
		t.Fatalf("%s does not end with %q: ...%s", name, cutWant(whole), got[max(0, len(got)-120):])
	}
	if !strings.HasPrefix(whole, prefix) || !utf8.ValidString(prefix) {
		t.Fatalf("%s keeps %d bytes that are not a whole-character prefix of the value", name, len(prefix))
	}
}

func TestBoundCE(t *testing.T) {
	envelope := []string{"specversion", "id", "type", "source", "time"}
	tests := []struct {
		name  string
		ce    func(t *testing.T) string
		check func(t *testing.T, in, out string)
	}{
		{
			name: "a record under the bound is stored byte for byte",
			ce: func(t *testing.T) string {
				small := recordOf(t, map[string]any{"command": "echo hi", "n": json.Number("12345678901234567890")})
				pad := MaxRecordBytes - len(small)
				return recordOf(t, map[string]any{"command": "echo hi" + strings.Repeat("x", pad), "n": json.Number("12345678901234567890")})
			},
			check: func(t *testing.T, in, out string) {
				if len(in) != MaxRecordBytes || out != in {
					t.Fatalf("a record of %d bytes came back changed (%d bytes)", len(in), len(out))
				}
			},
		},
		{
			name: "a written form json.Marshal would not write stays as written under the bound",
			ce:   func(*testing.T) string { return `{ "id" : "x", "data" : { "command" : "<" } }` },
			check: func(t *testing.T, in, out string) {
				if out != in {
					t.Fatalf("got %q, want the record unchanged", out)
				}
			},
		},
		{
			name: "a long command is cut with its length and hash and the rest is kept",
			ce: func(t *testing.T) string {
				return recordOf(t, map[string]any{"session": "s-1", "user": "u-1", "effect": "allow", "command": strings.Repeat("<", 900<<10)})
			},
			check: func(t *testing.T, in, out string) {
				m := decodeBound(t, out)
				data := m["data"].(map[string]any)
				checkCut(t, "command", data["command"].(string), strings.Repeat("<", 900<<10))
				if data["session"] != "s-1" || data["user"] != "u-1" || data["effect"] != "allow" {
					t.Fatalf("the short fields changed: %v", data)
				}
				in1 := decodeBound(t, in)
				for _, k := range envelope {
					if m[k] != in1[k] {
						t.Fatalf("envelope %s = %v, want %v", k, m[k], in1[k])
					}
				}
				// The cut keeps as much as fits: one more escaped < would not.
				if gap := MaxRecordBytes - len(out); gap < 0 || gap >= 6 {
					t.Fatalf("bounded record is %d bytes, want within 6 bytes under %d", len(out), MaxRecordBytes)
				}
			},
		},
		{
			name: "a character that straddles the cut is left out whole",
			ce: func(t *testing.T) string {
				return recordOf(t, map[string]any{"command": "ab" + strings.Repeat("日", 200<<10)})
			},
			check: func(t *testing.T, in, out string) {
				got := decodeBound(t, out)["data"].(map[string]any)["command"].(string)
				checkCut(t, "command", got, "ab"+strings.Repeat("日", 200<<10))
				if gap := MaxRecordBytes - len(out); gap < 0 || gap >= 3 {
					t.Fatalf("bounded record is %d bytes, want within one character under %d", len(out), MaxRecordBytes)
				}
			},
		},
		{
			name: "many medium strings are cut to one common length and short ones stay whole",
			ce: func(t *testing.T) string {
				data := map[string]any{"session": "s-1"}
				for i := range 400 {
					data[fmt.Sprintf("k%03d", i)] = fmt.Sprintf("%03d", i) + strings.Repeat("x", 2045)
				}
				return recordOf(t, data)
			},
			check: func(t *testing.T, _, out string) {
				data := decodeBound(t, out)["data"].(map[string]any)
				if data["session"] != "s-1" {
					t.Fatalf("session = %v, want it whole", data["session"])
				}
				lengths := map[int]int{}
				for i := range 400 {
					k := fmt.Sprintf("k%03d", i)
					v := data[k].(string)
					checkCut(t, k, v, fmt.Sprintf("%03d", i)+strings.Repeat("x", 2045))
					lengths[len(v)]++
				}
				if len(lengths) != 1 {
					t.Fatalf("the cut strings have %d different lengths, want one: %v", len(lengths), lengths)
				}
			},
		},
		{
			name: "strings in nested maps and arrays are cut and numbers and booleans kept",
			ce: func(t *testing.T) string {
				return recordOf(t, map[string]any{
					"paths":  []any{strings.Repeat("&", 50<<10), "/tmp/a"},
					"object": map[string]any{"type": "repo", "value": strings.Repeat("v", 300<<10)},
					"n":      json.Number("12345678901234567890"), "granted": true, "none": nil,
				})
			},
			check: func(t *testing.T, _, out string) {
				data := decodeBound(t, out)["data"].(map[string]any)
				paths := data["paths"].([]any)
				checkCut(t, "paths[0]", paths[0].(string), strings.Repeat("&", 50<<10))
				object := data["object"].(map[string]any)
				checkCut(t, "object.value", object["value"].(string), strings.Repeat("v", 300<<10))
				if paths[1] != "/tmp/a" || object["type"] != "repo" {
					t.Fatalf("short nested strings changed: %v %v", paths[1], object["type"])
				}
				if data["n"] != json.Number("12345678901234567890") || data["granted"] != true || data["none"] != nil {
					t.Fatalf("scalars changed: %v %v %v", data["n"], data["granted"], data["none"])
				}
			},
		},
		{
			name: "a hook record of an ordinary command and 3,000 long paths keeps its command",
			ce: func(t *testing.T) string {
				paths := make([]any, 3000)
				for i := range paths {
					paths[i] = fmt.Sprintf("/work/proj/%04d/", i) + strings.Repeat("p", 84)
				}
				return recordOf(t, map[string]any{"session": "s-1", "command": "git add -A", "workspace": "/work/proj", "paths": paths})
			},
			check: func(t *testing.T, _, out string) {
				data := decodeBound(t, out)["data"].(map[string]any)
				if data["dataOmitted"] == nil || data["command"] != "git add -A" || data["workspace"] != "/work/proj" {
					t.Fatalf("fallback lost the command or the workspace: %v", data)
				}
			},
		},
		{
			name: "an id over the bound is cut the same way on every write, so the record still dedupes",
			ce: func(t *testing.T) string {
				ce, err := json.Marshal(map[string]any{
					"specversion": "1.0", "id": strings.Repeat("i", 1200<<10), "type": "straza.audit.tool", "source": "straza",
					"time": map[string]any{"not": "a time"}, "data": map[string]any{"session": "s-1", "command": "ls"},
				})
				if err != nil {
					t.Fatal(err)
				}
				return string(ce)
			},
			check: func(t *testing.T, _, out string) {
				m := decodeBound(t, out)
				checkCut(t, "id", m["id"].(string), strings.Repeat("i", 1200<<10))
				if n := len(mustMarshal(t, m["id"])); n > omitKeepMax {
					t.Fatalf("kept id is %d bytes as JSON, over %d", n, omitKeepMax)
				}
				if _, ok := m["time"]; ok {
					t.Fatalf("fallback keeps a time that is not a string, number or boolean: %v", m["time"])
				}
				if data := m["data"].(map[string]any); data["command"] != "ls" || data["session"] != "s-1" {
					t.Fatalf("fallback lost the command or the session: %v", data)
				}
			},
		},
		{
			name: "a record that cutting cannot shrink keeps its envelope and identity and names the whole",
			ce: func(t *testing.T) string {
				paths := make([]any, 40_000)
				for i := range paths {
					paths[i] = fmt.Sprintf("/p/%06d", i)
				}
				ce, err := json.Marshal(map[string]any{
					"specversion": "1.0", "id": "rec-1", "type": "straza.audit.tool", "source": "straza",
					"time": "2026-10-31T09:14:02.118Z", "extension": strings.Repeat("e", 300<<10),
					"data": map[string]any{
						"session": "s-1", "user": "u-1", "effect": "deny", "count": json.Number("7"), "granted": false,
						"harness": strings.Repeat("h", 1000), "paths": paths, "object": map[string]any{"type": "repo"},
						"command": "make " + strings.Repeat("&", 600), "workspace": "/work/proj", "arguments": `{"q":"` + strings.Repeat("a", 900) + `"}`,
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				return string(ce)
			},
			check: func(t *testing.T, in, out string) {
				if len(out) > MaxRecordBytes {
					t.Fatalf("fallback record is %d bytes, over %d", len(out), MaxRecordBytes)
				}
				m := decodeBound(t, out)
				want := map[string]any{"specversion": "1.0", "id": "rec-1", "type": "straza.audit.tool", "source": "straza", "time": "2026-10-31T09:14:02.118Z"}
				for k, v := range want {
					if m[k] != v {
						t.Fatalf("envelope %s = %v, want %v", k, m[k], v)
					}
				}
				if _, ok := m["extension"]; ok || len(m) != len(want)+1 {
					t.Fatalf("fallback keeps attributes beyond the envelope: %v", m)
				}
				data := m["data"].(map[string]any)
				sum := sha256.Sum256([]byte(in))
				if got := data["dataOmitted"]; got != fmt.Sprintf("%d bytes, sha256 %s", len(in), hex.EncodeToString(sum[:])) {
					t.Fatalf("dataOmitted = %v", got)
				}
				if data["session"] != "s-1" || data["user"] != "u-1" || data["effect"] != "deny" {
					t.Fatalf("identity fields lost: %v", data)
				}
				checkCut(t, "harness", data["harness"].(string), strings.Repeat("h", 1000))
				checkCut(t, "command", data["command"].(string), "make "+strings.Repeat("&", 600))
				checkCut(t, "arguments", data["arguments"].(string), `{"q":"`+strings.Repeat("a", 900)+`"}`)
				if data["workspace"] != "/work/proj" {
					t.Fatalf("workspace = %v, want it whole", data["workspace"])
				}
				if n := len(mustMarshal(t, data["harness"])); n > omitKeepMax {
					t.Fatalf("kept harness is %d bytes as JSON, over %d", n, omitKeepMax)
				}
				for _, k := range []string{"paths", "object", "count", "granted"} {
					if _, ok := data[k]; ok {
						t.Fatalf("fallback keeps %s, which is not an identity field", k)
					}
				}
			},
		},
		{
			name: "a record that is not a JSON object is stored whole",
			ce:   func(*testing.T) string { return strings.Repeat("<", MaxRecordBytes+1) },
			check: func(t *testing.T, in, out string) {
				if out != in {
					t.Fatalf("got %d bytes, want the %d bytes unchanged", len(out), len(in))
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.ce(t)
			out := boundCE(in)
			if len(in) > MaxRecordBytes && json.Valid([]byte(in)) && len(out) > MaxRecordBytes {
				t.Fatalf("bounded record is %d bytes, over %d", len(out), MaxRecordBytes)
			}
			if again := boundCE(in); again != out {
				t.Fatal("boundCE gave two answers for one record")
			}
			tc.check(t, in, out)
		})
	}
}

// TestOutboxWritesBoundOutsideTheTransaction pins that Insert, InsertBatch
// and a device revocation cut their records before they take the store. On
// SQLite's one connection a read made from inside the cut answers at once,
// where a cut inside the open transaction would wait for the connection it
// holds until the read's deadline.
func TestOutboxWritesBoundOutsideTheTransaction(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		if s.(*sqlStore).d != dialectSQLite {
			t.Skip("only the one SQLite connection shows a cut made inside the transaction")
		}
		ctx := context.Background()
		cuts := 0
		boundRecord = func(ce string) string {
			cuts++
			rctx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			if _, err := s.Outbox().Head(rctx); err != nil {
				t.Errorf("a read during the cut waited for the store: %v", err)
			}
			return boundCE(ce)
		}
		t.Cleanup(func() { boundRecord = boundCE })
		big := recordOf(t, map[string]any{"command": strings.Repeat("<", 100<<10)})
		if _, err := s.Outbox().Insert(ctx, OutboxEvent{Subject: "straza.audit.tool", CE: big}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Outbox().InsertBatch(ctx, []OutboxEvent{{Subject: "straza.audit.tool", CE: big}, {Subject: "straza.audit.tool", CE: big}}); err != nil {
			t.Fatal(err)
		}
		u, err := s.Users().Create(ctx, User{Username: "dev-revoke"})
		if err != nil {
			t.Fatal(err)
		}
		d, err := s.Devices().Create(ctx, Device{UserID: u.ID, Name: "laptop", Fingerprint: "fp-revoke"})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Devices().Revoke(ctx, d.ID, "lost", OutboxEvent{Subject: "straza.revocation.device", CE: big}); err != nil {
			t.Fatal(err)
		}
		if cuts < 4 {
			t.Fatalf("the writes called the bound %d times, want at least 4", cuts)
		}
		rows, err := s.Outbox().ListRecent(ctx, 10)
		if err != nil || len(rows) != 4 {
			t.Fatalf("outbox rows = %d, %v, want 4", len(rows), err)
		}
		for _, r := range rows {
			if len(r.CE) > MaxRecordBytes {
				t.Fatalf("row %s is %d bytes, over the bound", r.ID, len(r.CE))
			}
		}
	})
}

// TestJSONLenAgreesWithMarshal pins jsonLen, which sizes a cut without
// marshalling, to the bytes encoding/json writes.
func TestJSONLenAgreesWithMarshal(t *testing.T) {
	var ascii strings.Builder
	for b := range utf8.RuneSelf {
		ascii.WriteByte(byte(b))
	}
	for _, s := range []string{"", ascii.String(), "日本\u2028\u2029é\U0001F600", "bad\xff\xfebytes", `"\<>&`} {
		if got, want := jsonLen(s), len(mustMarshal(t, s)); got != want {
			t.Errorf("jsonLen(%q) = %d, json.Marshal writes %d", s, got, want)
		}
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
