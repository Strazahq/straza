package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/audit"
	"github.com/strazahq/straza/internal/store"
)

// decisionSeed is one chained record the overview decision block reads: a
// decision record, or with ceType straza.audit.approval a request record
// whose summary names the call.
type decisionSeed struct {
	ceType  string
	when    time.Time
	app     string
	tool    string
	effect  string
	reason  string
	summary string
	// rule is the rule id the record carries; empty means r1, the rule the
	// held pairs share.
	rule  string
	count int
	// event is the event kind of a list or view record, which names no tool:
	// tools.list, resources.list or resources.read. Empty means tool.pre.
	event string
	// noEvent leaves data.event out of the record.
	noEvent bool
}

// seedDecisions chains the seeds as the emitters write them: a hook-lane
// record is straza.audit.tool with the data keys pdp.go spools, a gateway
// record is straza.audit.mcp with the keys gateway_tools.go spools.
func seedDecisions(t *testing.T, app *App, seeds []decisionSeed) {
	t.Helper()
	ctx := context.Background()
	var events []store.ChainEvent
	// The chain dedupes by CE id, so every call mints its own id space.
	batch := time.Now().UnixNano()
	n := 0
	flush := func() {
		t.Helper()
		if len(events) == 0 {
			return
		}
		if _, err := app.store.Audit().AppendChained(ctx, events, audit.Genesis, audit.Link); err != nil {
			t.Fatalf("AppendChained: %v", err)
		}
		events = events[:0]
	}
	for _, s := range seeds {
		rule := s.rule
		if rule == "" {
			rule = "r1"
		}
		for i := 0; i < s.count; i++ {
			n++
			id := fmt.Sprintf("ce-dec-%d-%05d", batch, n)
			if s.ceType == "straza.audit.approval" {
				events = append(events, store.ChainEvent{CEID: id, CE: fmt.Sprintf(
					`{"specversion":"1.0","id":%q,"type":"straza.audit.approval","source":"strazad","time":%q,`+
						`"data":{"phase":"request","approvalId":%q,"state":"pending","session":"s1","user":"u1",`+
						`"rule":"r1","set":"baseline","lane":"gateway","summary":%q}}`,
					id, s.when.Format(time.RFC3339Nano), id, s.summary)})
				if len(events) == 500 {
					flush()
				}
				continue
			}
			// A gateway record names the event kind in tool and the resolved
			// tool in toolName; a hook record names its tool in tool alone.
			tool, toolName := s.tool, ""
			if s.ceType == "straza.audit.mcp" {
				tool, toolName = "mcp.call", s.tool
			}
			event := `"event":"tool.pre",`
			switch {
			case s.noEvent:
				event = ""
			case s.event != "":
				event, tool, toolName = fmt.Sprintf(`"event":%q,`, s.event), "", ""
			}
			events = append(events, store.ChainEvent{CEID: id, CE: fmt.Sprintf(
				`{"specversion":"1.0","id":%q,"type":%q,"source":"strazad","time":%q,`+
					`"data":{"session":"s1","user":"u1","harness":"claude-code/2.1.0",%s`+
					`"tool":%q,"app":%q,"toolName":%q,"effect":%q,"ruleId":%q,`+
					`"setName":"baseline","reason":%q,"snapshot":"snap-1"}}`,
				id, s.ceType, s.when.Format(time.RFC3339Nano), event, tool, s.app, toolName, s.effect, rule, s.reason)})
			if len(events) == 500 {
				flush()
			}
		}
	}
	flush()
}

// TestOverviewDecisionBlock drives GET /v1/admin/overview: the decision
// block is 24 hour-aligned buckets ending with the current hour, empty
// hours carry zeros, the effect words map to the three outcomes the console
// reads, an allowed catalog read counts apart from them, the stopped list is
// the busiest five groups with their most frequent reason verbatim, and the
// whole block is cached for a minute so a 30 second poll costs one window
// scan.
func TestOverviewDecisionBlock(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")

	hour := time.Now().UTC().Truncate(time.Hour)
	since := hour.Add(-23 * time.Hour)
	at := func(bucket int) time.Time { return since.Add(time.Duration(bucket)*time.Hour + time.Minute) }

	seeds := []decisionSeed{
		// Records the block must not count: the wrong type, a decision from
		// before the window, and one dated after it.
		{ceType: "straza.audit.admin", when: at(4), effect: "allow", count: 1},
		{ceType: "straza.audit.mcp", when: since.Add(-90 * time.Minute), app: "files", tool: "mcp.call", effect: "deny", reason: "too old", count: 3},
		{ceType: "straza.audit.mcp", when: hour.Add(2 * time.Hour), app: "files", tool: "mcp.call", effect: "deny", reason: "from the future", count: 3},
		// A held gateway call is its approval request record and then the
		// pre-hold verdict, allow, spooled after the hold; a held hook call
		// is the request and then the hold recorded as deny. Both outcomes
		// are approval and both land in the same app and tool group.
		{ceType: "straza.audit.approval", when: at(3), summary: "mcp.call github:read", count: 4},
		{ceType: "straza.audit.mcp", when: at(3).Add(90 * time.Second), app: "github", tool: "mcp.call", effect: "allow", reason: "Straza: a human decides", count: 4},
		{ceType: "straza.audit.approval", when: at(10), summary: "mcp.call github:read", count: 3},
		{ceType: "straza.audit.tool", when: at(10).Add(2 * time.Second), app: "github", tool: "mcp.call", effect: "deny", reason: "Straza: approval requested", count: 3},
		// A request no decision record answered: counted by its own time
		// and named by its summary.
		{ceType: "straza.audit.approval", when: at(15), summary: "mcp.call db:write", count: 1},
		// A request older than the match window does not re-label the
		// decision that follows it.
		{ceType: "straza.audit.approval", when: at(17), summary: "mcp.call db:write", count: 1},
		{ceType: "straza.audit.mcp", when: at(17).Add(11 * time.Minute), app: "db", tool: "mcp.call", effect: "allow", reason: "Straza: allowed late", count: 1},
		// The hook lane records no app; the empty name is its own group.
		{ceType: "straza.audit.tool", when: at(3), tool: "shell.exec", effect: "deny", reason: "Straza: rm is blocked", rule: "r-deny", count: 5},
		{ceType: "straza.audit.tool", when: at(3), tool: "shell.exec", effect: "deny", reason: "Straza: no rule allows it", rule: "r-deny", count: 2},
		{ceType: "straza.audit.mcp", when: at(20), app: "db", tool: "mcp.call", effect: "deny", reason: "Straza: writes are denied", rule: "r-deny", count: 2},
		// Three single denies: the sixth group falls off the list of five,
		// and the survivors are ordered by app name.
		{ceType: "straza.audit.mcp", when: at(23), app: "aaa", tool: "mcp.call", effect: "deny", reason: "Straza: aaa", rule: "r-deny", count: 1},
		{ceType: "straza.audit.mcp", when: at(23), app: "mmm", tool: "mcp.call", effect: "deny", reason: "Straza: mmm", rule: "r-deny", count: 1},
		{ceType: "straza.audit.mcp", when: at(23), app: "zzz", tool: "mcp.call", effect: "deny", reason: "Straza: zzz", rule: "r-deny", count: 1},
		// A catalog read is the gateway's record of a tools/list or a
		// resources/list answer. It counts apart from the allowed calls.
		{ceType: "straza.audit.mcp", when: at(5), event: "tools.list", effect: "allow", reason: "catalog served", rule: "r-none", count: 6},
		{ceType: "straza.audit.mcp", when: at(5), app: "files", event: "resources.list", effect: "allow", reason: "views served", rule: "r-none", count: 3},
		// A view read serves one view and can be refused, so an allowed one
		// counts with the allowed calls. A record with no event key does too.
		{ceType: "straza.audit.mcp", when: at(7), app: "files", event: "resources.read", effect: "allow", reason: "view served", rule: "r-none", count: 2},
		{ceType: "straza.audit.mcp", when: at(7), app: "files", tool: "mcp.call", effect: "allow", reason: "Straza: allowed by rule", rule: "r-files", noEvent: true, count: 4},
		// Only the gateway writes a catalog read: a hook-lane record that
		// names the event counts as any other allowed decision.
		{ceType: "straza.audit.tool", when: at(8), event: "tools.list", effect: "allow", reason: "catalog served", rule: "r-none", count: 2},
		// A denied or a held record is never a catalog read. Both groups
		// sort after the five the stopped list keeps.
		{ceType: "straza.audit.mcp", when: at(8), app: "zzzz", event: "tools.list", effect: "deny", reason: "Straza: zzzz", rule: "r-deny", count: 1},
		{ceType: "straza.audit.approval", when: at(9), summary: "tools.list zzzz", count: 1},
		{ceType: "straza.audit.mcp", when: at(9).Add(time.Second), app: "zzzz", event: "tools.list", effect: "allow", reason: "catalog served", count: 1},
	}
	// 200 allows an hour from a plain allow rule: the bulk of the window,
	// and the reason every bucket carries the same floor.
	for b := 0; b < 24; b++ {
		seeds = append(seeds, decisionSeed{ceType: "straza.audit.mcp", when: at(b),
			app: "files", tool: "mcp.call", effect: "allow", reason: "Straza: allowed by rule", rule: "r-files", count: 200})
	}
	seedDecisions(t, app, seeds)

	type bucket struct {
		Start    time.Time `json:"start"`
		Allowed  int       `json:"allowed"`
		Approval int       `json:"approval"`
		Denied   int       `json:"denied"`
		Catalog  int       `json:"catalog"`
	}
	type stopped struct {
		App     string `json:"app"`
		Tool    string `json:"tool"`
		Outcome string `json:"outcome"`
		Count   int    `json:"count"`
		Reason  string `json:"reason"`
	}
	var out struct {
		Decisions struct {
			Since   time.Time `json:"since"`
			Buckets []bucket  `json:"buckets"`
			Stopped []stopped `json:"stopped"`
		} `json:"decisions"`
	}
	get := func() {
		t.Helper()
		if code := adminReq(t, http.MethodGet, base+"/v1/admin/overview", idToken, nil, &out); code != http.StatusOK {
			t.Fatalf("overview = %d", code)
		}
	}
	get()
	if !out.Decisions.Since.Equal(since) {
		// The seeds are placed against the hour the test started in. A roll
		// over that boundary is not a failure of the handler.
		t.Skipf("the hour rolled during the test: since = %s, seeded against %s", out.Decisions.Since, since)
	}
	if len(out.Decisions.Buckets) != 24 {
		t.Fatalf("buckets = %d, want 24", len(out.Decisions.Buckets))
	}

	wantApproval := map[int]int{3: 4, 9: 1, 10: 3, 15: 1, 17: 1}
	wantDenied := map[int]int{3: 7, 8: 1, 20: 2, 23: 3}
	wantAllowed := map[int]int{7: 206, 8: 202, 17: 201}
	wantCatalog := map[int]int{5: 9}
	for i, b := range out.Decisions.Buckets {
		if want := since.Add(time.Duration(i) * time.Hour); !b.Start.Equal(want) {
			t.Errorf("bucket %d starts %s, want %s", i, b.Start, want)
		}
		allowed, ok := wantAllowed[i]
		if !ok {
			allowed = 200
		}
		if b.Allowed != allowed {
			t.Errorf("bucket %d allowed = %d, want %d", i, b.Allowed, allowed)
		}
		if b.Approval != wantApproval[i] {
			t.Errorf("bucket %d approval = %d, want %d", i, b.Approval, wantApproval[i])
		}
		if b.Denied != wantDenied[i] {
			t.Errorf("bucket %d denied = %d, want %d", i, b.Denied, wantDenied[i])
		}
		if b.Catalog != wantCatalog[i] {
			t.Errorf("bucket %d catalog = %d, want %d", i, b.Catalog, wantCatalog[i])
		}
	}

	want := []stopped{
		{App: "", Tool: "shell.exec", Outcome: "denied", Count: 7, Reason: "Straza: rm is blocked"},
		{App: "github", Tool: "mcp.call", Outcome: "approval", Count: 7, Reason: "Straza: a human decides"},
		{App: "", Tool: "mcp.call db:write", Outcome: "approval", Count: 2, Reason: ""},
		{App: "db", Tool: "mcp.call", Outcome: "denied", Count: 2, Reason: "Straza: writes are denied"},
		{App: "aaa", Tool: "mcp.call", Outcome: "denied", Count: 1, Reason: "Straza: aaa"},
	}
	if len(out.Decisions.Stopped) != len(want) {
		t.Fatalf("stopped = %+v, want %d entries", out.Decisions.Stopped, len(want))
	}
	for i, w := range want {
		if out.Decisions.Stopped[i] != w {
			t.Errorf("stopped[%d] = %+v, want %+v", i, out.Decisions.Stopped[i], w)
		}
	}

	// The cache: records chained after the first read stay invisible until
	// the entry ages out, and show up on the first read after it does.
	seedDecisions(t, app, []decisionSeed{{ceType: "straza.audit.mcp", when: at(23),
		app: "files", tool: "mcp.call", effect: "deny", reason: "Straza: late", rule: "r-deny", count: 40}})
	get()
	if got := out.Decisions.Buckets[23].Denied; got != 3 {
		t.Errorf("bucket 23 denied = %d after a cached read, want the cached 3", got)
	}
	app.decisions.mu.Lock()
	app.decisions.at = time.Now().UTC().Add(-2 * decisionsCacheTTL)
	app.decisions.mu.Unlock()
	get()
	if got := out.Decisions.Buckets[23].Denied; got != 43 {
		t.Errorf("bucket 23 denied = %d after the cache aged out, want 43", got)
	}
	if len(out.Decisions.Stopped) == 0 || out.Decisions.Stopped[0].Reason != "Straza: late" {
		t.Errorf("stopped[0] = %+v, want the 40 late denials first", out.Decisions.Stopped)
	}
}

// TestOverviewDecisionMinutes drives GET /v1/admin/overview?window=hour: the
// decisions block then also carries 60 minute-aligned buckets ending with
// the current minute, counted by the rules of the day block, the read
// without the parameter carries no minutes key, any other window is refused
// with a sentence, and the minutes are cached for five seconds so many
// viewers cost one scan.
func TestOverviewDecisionMinutes(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")

	minute := time.Now().UTC().Truncate(time.Minute)
	since := minute.Add(-59 * time.Minute)
	at := func(bucket int) time.Time { return since.Add(time.Duration(bucket)*time.Minute + time.Second) }
	seedDecisions(t, app, []decisionSeed{
		// A record dated before the hour and one dated after it stay out.
		{ceType: "straza.audit.mcp", when: since.Add(-90 * time.Second), app: "files", tool: "mcp.call", effect: "allow", reason: "too old", rule: "r-files", count: 3},
		{ceType: "straza.audit.mcp", when: minute.Add(2 * time.Minute), app: "files", tool: "mcp.call", effect: "allow", reason: "from the future", rule: "r-files", count: 3},
		// A record lands in the minute of its time.
		{ceType: "straza.audit.mcp", when: at(0), app: "files", tool: "mcp.call", effect: "allow", reason: "Straza: allowed by rule", rule: "r-files", count: 2},
		{ceType: "straza.audit.mcp", when: at(30), app: "files", tool: "mcp.call", effect: "allow", reason: "Straza: allowed by rule", rule: "r-files", count: 5},
		{ceType: "straza.audit.tool", when: at(30), tool: "shell.exec", effect: "deny", reason: "Straza: rm is blocked", rule: "r-deny", count: 2},
		// A catalog read counts apart in its minute, and a held call is
		// paired with its request, as in the day block.
		{ceType: "straza.audit.mcp", when: at(30), event: "tools.list", effect: "allow", reason: "catalog served", rule: "r-none", count: 4},
		{ceType: "straza.audit.approval", when: at(45), summary: "mcp.call github:read", count: 1},
		{ceType: "straza.audit.mcp", when: at(45).Add(2 * time.Second), app: "github", tool: "mcp.call", effect: "allow", reason: "Straza: a human decides", count: 1},
		{ceType: "straza.audit.mcp", when: at(59), app: "files", tool: "mcp.call", effect: "allow", reason: "Straza: allowed by rule", rule: "r-files", count: 7},
	})

	// Without the parameter the block is the day alone.
	var plain struct {
		Decisions map[string]json.RawMessage `json:"decisions"`
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/overview", idToken, nil, &plain); code != http.StatusOK {
		t.Fatalf("overview = %d", code)
	}
	if _, ok := plain.Decisions["minutes"]; ok || plain.Decisions["buckets"] == nil {
		t.Errorf("decisions without window = %v, want buckets and no minutes key", plain.Decisions)
	}

	type bucket struct {
		Start    time.Time `json:"start"`
		Allowed  int       `json:"allowed"`
		Approval int       `json:"approval"`
		Denied   int       `json:"denied"`
		Catalog  int       `json:"catalog"`
	}
	var out struct {
		Decisions struct {
			Buckets []bucket          `json:"buckets"`
			Stopped []json.RawMessage `json:"stopped"`
			Minutes []bucket          `json:"minutes"`
		} `json:"decisions"`
	}
	get := func() {
		t.Helper()
		if code := adminReq(t, http.MethodGet, base+"/v1/admin/overview?window=hour", idToken, nil, &out); code != http.StatusOK {
			t.Fatalf("overview?window=hour = %d", code)
		}
		if len(out.Decisions.Minutes) != 60 {
			t.Fatalf("minutes = %d, want 60", len(out.Decisions.Minutes))
		}
	}
	get()
	if !out.Decisions.Minutes[59].Start.Equal(minute) {
		// The seeds are placed against the minute the test started in. A
		// roll over that boundary is not a failure of the handler.
		t.Skipf("the minute rolled during the test: the last minute starts %s, seeded against %s", out.Decisions.Minutes[59].Start, minute)
	}
	want := map[int]bucket{
		0:  {Allowed: 2},
		30: {Allowed: 5, Denied: 2, Catalog: 4},
		45: {Approval: 1},
		59: {Allowed: 7},
	}
	for i, b := range out.Decisions.Minutes {
		w := want[i]
		w.Start = since.Add(time.Duration(i) * time.Minute)
		if !b.Start.Equal(w.Start) || b.Allowed != w.Allowed || b.Approval != w.Approval || b.Denied != w.Denied || b.Catalog != w.Catalog {
			t.Errorf("minute %d = %+v, want %+v", i, b, w)
		}
	}
	// The day's block rides along unchanged, the stopped list included.
	if len(out.Decisions.Buckets) != 24 || len(out.Decisions.Stopped) != 2 {
		t.Errorf("buckets = %d and stopped = %d on a window=hour read, want 24 and 2", len(out.Decisions.Buckets), len(out.Decisions.Stopped))
	}

	for _, window := range []string{"day", "minute", "HOUR", "60"} {
		var refusal struct {
			Error string `json:"error"`
		}
		code := adminReq(t, http.MethodGet, base+"/v1/admin/overview?window="+window, idToken, nil, &refusal)
		const sentence = "window takes one value, hour, and this request sent another. " +
			"Send window=hour to add the last hour counted by the minute, or leave window out to read the last 24 hours alone."
		if code != http.StatusBadRequest || refusal.Error != sentence {
			t.Errorf("window=%s = %d %q, want 400 and the sentence that names hour", window, code, refusal.Error)
		}
	}

	// The cache: records chained after the first read stay invisible until
	// the entry ages out. The minute is found by its start, because the
	// window may have moved on by then.
	seedDecisions(t, app, []decisionSeed{{ceType: "straza.audit.mcp", when: at(59),
		app: "files", tool: "mcp.call", effect: "allow", reason: "Straza: late", rule: "r-files", count: 40}})
	allowedThen := func() int {
		t.Helper()
		get()
		for _, b := range out.Decisions.Minutes {
			if b.Start.Equal(minute) {
				return b.Allowed
			}
		}
		t.Fatalf("no minute starts %s in %+v", minute, out.Decisions.Minutes)
		return 0
	}
	if got := allowedThen(); got != 7 {
		t.Errorf("the seeded minute allowed = %d after a cached read, want the cached 7", got)
	}
	app.decisions.minutes.mu.Lock()
	app.decisions.minutes.at = time.Now().UTC().Add(-2 * minutesCacheTTL)
	app.decisions.minutes.mu.Unlock()
	if got := allowedThen(); got != 47 {
		t.Errorf("the seeded minute allowed = %d after the cache aged out, want 47", got)
	}
}

// TestDecisionOutcomeWords pins the effect word each outcome counts. allow
// and deny are what the engine writes today; approve and confirm are the
// mode words a held call carries, the pair web/ui/src/lib/audit-words.ts
// reads as held. An effect the server does not know is counted nowhere.
func TestDecisionOutcomeWords(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ effect, want string }{
		{"allow", outcomeAllowed},
		{"deny", outcomeDenied},
		{"approve", outcomeApproval},
		{"confirm", outcomeApproval},
		{"", ""},
		{"maybe", ""},
	} {
		if got := decisionOutcome(tc.effect); got != tc.want {
			t.Errorf("decisionOutcome(%q) = %q, want %q", tc.effect, got, tc.want)
		}
	}
}
