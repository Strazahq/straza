package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestAuditBatchRefusesMoreThanAThousandEvents pins the record cap of one
// upload: 1000 records are stored, 1001 are refused with 413 and a sentence,
// nothing of the refused upload is stored, and one Warn line names the
// uploader.
func TestAuditBatchRefusesMoreThanAThousandEvents(t *testing.T) {
	t.Parallel()
	log, buf := captureLogger()
	app, base := testAppPreRun(t, []func(*App){func(a *App) { a.log = log }})
	seedIdentity(t, app)
	token, session := checkinToken(t, app, base)
	upload := func(n int, prefix string) (int, map[string]any) {
		events := make([]map[string]any, n)
		for i := range events {
			events[i] = map[string]any{"data": map[string]any{"event": "tool.pre", "tool": "shell.exec", "command": fmt.Sprintf("%s-%d", prefix, i)}}
		}
		return postJSONAuth(t, base+"/v1/audit/batch", token, map[string]any{"events": events})
	}

	code, resp := upload(1001, "over-cap")
	msg, _ := resp["error"].(string)
	if code != http.StatusRequestEntityTooLarge || !strings.Contains(msg, "more than 1000 records") || !strings.Contains(msg, "none of its records were stored") {
		t.Fatalf("an upload of 1001 records = %d %q, want 413 with the sentence", code, msg)
	}
	if code, resp := upload(1000, "at-cap"); code != http.StatusOK || resp["accepted"] != float64(1000) {
		t.Fatalf("an upload of 1000 records = %d %v, want all accepted", code, resp)
	}
	waitChainData(t, app, "at-cap-0")
	if d := chainData(app, "over-cap-0"); d != nil {
		t.Fatalf("a record of the refused upload reached the chain: %v", d)
	}
	if n := warnLines(buf, "more than 1000 records", "session="+session); n != 1 {
		t.Fatalf("Warn lines about the refused upload = %d, want 1:\n%s", n, buf.String())
	}
}

// TestBatchDecodeStopsAfterTheCap pins that the decode of an upload stops at
// the record past the cap: a 4 MiB upload of 1.4 million empty records is
// refused for a few thousand allocations, not one or more per record. The
// array keeps the field rules of encoding/json: null holds no records, the
// key matches without regard to case, and a value that is not an array is
// malformed. It runs serially, because testing.AllocsPerRun refuses a
// parallel test.
func TestBatchDecodeStopsAfterTheCap(t *testing.T) {
	huge := []byte(`{"events":[` + strings.Repeat(`{},`, 1400000) + `{}]}`)
	var req batchRequest
	if err := json.Unmarshal(huge, &req); !errors.Is(err, errTooManyEvents) {
		t.Fatalf("decoding 1.4 million records answered %v, want errTooManyEvents", err)
	}
	if allocs := testing.AllocsPerRun(2, func() { _ = json.Unmarshal(huge, &batchRequest{}) }); allocs > 10000 {
		t.Fatalf("refusing the upload took %.0f allocations, so it decoded every record", allocs)
	}
	cases := []struct {
		name, body string
		want       int
		wantErr    bool
	}{
		{"exactly the cap", `{"events":[` + strings.Repeat(`{},`, 999) + `{}]}`, 1000, false},
		{"null", `{"events":null}`, 0, false},
		{"no events key", `{}`, 0, false},
		{"the key in another case", `{"Events":[{},{}]}`, 2, false},
		{"not an array", `{"events":"x"}`, 0, true},
		{"a broken record", `{"events":[{]}`, 0, true},
	}
	for _, c := range cases {
		var req batchRequest
		err := json.Unmarshal([]byte(c.body), &req)
		if (err != nil) != c.wantErr || len(req.Events) != c.want {
			t.Errorf("%s: %d records and error %v, want %d records and an error %v", c.name, len(req.Events), err, c.want, c.wantErr)
		}
	}
}
