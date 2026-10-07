package server

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/strazahq/straza/internal/policy"
)

// The decision block of the overview: what policy did over the last day and
// what it stopped most often, and on request the last hour by the minute.
const (
	decisionHours      = 24
	decisionMinutes    = 60
	decisionScanBatch  = 1000
	decisionStoppedMax = 5
	decisionsCacheTTL  = 60 * time.Second
	minutesCacheTTL    = 5 * time.Second
)

// overviewWindowHour is the value of the overview's window parameter that
// adds the last hour's minutes to the decisions block.
const overviewWindowHour = "hour"

// overviewWindowMsg refuses any other value of the window parameter.
const overviewWindowMsg = "window takes one value, hour, and this request sent another. " +
	"Send window=hour to add the last hour counted by the minute, or leave window out to read the last 24 hours alone."

// Outcome words of the decision block. They are the console's reading of a
// record, not the wire: the wire word is data.effect, mapped by
// decisionOutcome.
const (
	outcomeAllowed  = "allowed"
	outcomeApproval = "approval"
	outcomeDenied   = "denied"
)

// decisionOutcome maps the effect a decision record carries to the outcome
// the block counts. allow and deny are what the engine writes today
// (policy.Decision.Effect). A held call carries no effect word of its own:
// the gateway records the pre-hold verdict (allow) and the hook lane the
// hold itself (deny), and the hold is its own straza.audit.approval request
// record, so countDecisions re-labels a decision as approval by that record
// and the mode words here only cover a future emitter that writes them. An
// effect the server does not know is counted nowhere, never guessed.
func decisionOutcome(effect string) string {
	switch effect {
	case policy.EffectAllow:
		return outcomeAllowed
	case policy.EffectDeny:
		return outcomeDenied
	case policy.ModeApprove, policy.ModeConfirm:
		return outcomeApproval
	}
	return ""
}

type decisionBucketPayload struct {
	Start    time.Time `json:"start"`
	Allowed  int       `json:"allowed"`
	Approval int       `json:"approval"`
	Denied   int       `json:"denied"`
	// Catalog counts the bucket's allowed catalog reads, which Allowed
	// leaves out.
	Catalog int `json:"catalog"`
}

type stoppedCallPayload struct {
	App     string `json:"app"`
	Tool    string `json:"tool"`
	Outcome string `json:"outcome"`
	Count   int    `json:"count"`
	Reason  string `json:"reason"`
}

// decisionsPayload is one day of decisions: an hour-aligned histogram that
// always carries decisionHours entries, empty hours included, and the calls
// policy stopped most often over the same window.
type decisionsPayload struct {
	Since   time.Time               `json:"since"`
	Buckets []decisionBucketPayload `json:"buckets"`
	Stopped []stoppedCallPayload    `json:"stopped"`
	// Minutes is the last hour by the minute: decisionMinutes entries ending
	// with the current minute. Only a read that asks for the hour window
	// carries it.
	Minutes []decisionBucketPayload `json:"minutes,omitempty"`
}

// decisionCE is the slice of a chained CloudEvent the block reads: the
// decision keys of a tool or mcp record, and the request keys of an
// approval record. Every other field, the arguments included, stays
// undecoded.
type decisionCE struct {
	Type string    `json:"type"`
	Time time.Time `json:"time"`
	Data struct {
		Session string `json:"session"`
		Tool    string `json:"tool"`
		// ToolName is the tool the gateway resolved on an MCP record, where
		// Tool is the tool word mcp.call; a hook record names its tool in
		// Tool and carries no ToolName.
		ToolName string `json:"toolName"`
		App      string `json:"app"`
		Effect   string `json:"effect"`
		Reason   string `json:"reason"`
		RuleID   string `json:"ruleId"`
		Phase    string `json:"phase"`
		Rule     string `json:"rule"`
		Summary  string `json:"summary"`
		// Event is tool.pre on a call. A gateway list or view record names
		// its kind here (tools.list, resources.list or resources.read) and
		// no tool.
		Event string `json:"event"`
	} `json:"data"`
}

// catalogRead reports whether ce is the gateway's record of a tools/list or
// a resources/list answer. Such an answer lists what the caller may use and
// runs no tool, so countDecisions counts an allowed one apart from the
// allowed calls. A resources.read record is not a catalog read: it serves
// one view and can be refused, so it counts as allowed or denied. The type
// is part of the test because a client can upload a straza.audit.tool
// record with any event word.
func catalogRead(ce decisionCE) bool {
	return ce.Type == "straza.audit.mcp" && (ce.Data.Event == "tools.list" || ce.Data.Event == eventResourcesList)
}

// pendingRequest is one approval request record waiting for the decision
// record of the same session and rule, which the lanes spool after the
// hold resolves.
type pendingRequest struct {
	at      time.Time
	summary string
}

// approvalMatchWindow bounds how long after its request record a decision
// record still counts as that held call: twice the gateway hold, and at
// least ten minutes so a raised hold or a slow outbox never splits a pair.
func (a *App) approvalMatchWindow() time.Duration {
	w := 10 * time.Minute
	if h := 2 * time.Duration(a.cfg.Approval.GatewayHoldSeconds) * time.Second; h > w {
		w = h
	}
	return w
}

// takePending pops the oldest request of the key that precedes at within
// the window, or nil when the decision was not held.
func takePending(pending map[string][]pendingRequest, key string, at time.Time, window time.Duration) *pendingRequest {
	reqs := pending[key]
	for i, r := range reqs {
		if !r.at.After(at) && at.Sub(r.at) <= window {
			pending[key] = append(reqs[:i], reqs[i+1:]...)
			return &r
		}
	}
	return nil
}

// decisionsCache holds the finished block for decisionsCacheTTL. The window
// read is a scan, so a dashboard polling every 30 seconds costs one scan a
// minute; the mutex is held across the scan so concurrent pollers wait for
// that one scan instead of starting their own.
type decisionsCache struct {
	mu      sync.Mutex
	at      time.Time
	block   decisionsPayload
	minutes minutesCache
}

// minutesCache holds the last hour's buckets for minutesCacheTTL. It has its
// own mutex, held across the scan like the day's, so a viewer of the last
// hour never waits for the day's scan and all of them share one scan per
// TTL.
type minutesCache struct {
	mu      sync.Mutex
	at      time.Time
	buckets []decisionBucketPayload
}

// stoppedGroup accumulates one (app, tool, outcome) group of stopped calls.
type stoppedGroup struct {
	app, tool, outcome string
	count              int
	reasons            map[string]int
}

// decisionsBlock counts the last decisionHours hour-aligned hours, ending
// with the current hour, and lists the calls policy stopped most often in
// them.
func (a *App) decisionsBlock(ctx context.Context) (decisionsPayload, error) {
	a.decisions.mu.Lock()
	defer a.decisions.mu.Unlock()
	now := time.Now().UTC()
	if !a.decisions.at.IsZero() && now.Sub(a.decisions.at) < decisionsCacheTTL {
		return a.decisions.block, nil
	}
	since := now.Truncate(time.Hour).Add(-time.Duration(decisionHours-1) * time.Hour)
	buckets, groups, err := a.countDecisions(ctx, since, time.Hour, decisionHours)
	if err != nil {
		return decisionsPayload{}, err
	}
	out := decisionsPayload{Since: since, Buckets: buckets, Stopped: topStopped(groups)}
	a.decisions.block, a.decisions.at = out, now
	return out, nil
}

// minutesBlock counts the last decisionMinutes minute-aligned minutes,
// ending with the current minute, by the rules of the day block. It scans
// the last hour only.
func (a *App) minutesBlock(ctx context.Context) ([]decisionBucketPayload, error) {
	c := &a.decisions.minutes
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now().UTC()
	if !c.at.IsZero() && now.Sub(c.at) < minutesCacheTTL {
		return c.buckets, nil
	}
	since := now.Truncate(time.Minute).Add(-time.Duration(decisionMinutes-1) * time.Minute)
	buckets, _, err := a.countDecisions(ctx, since, time.Minute, decisionMinutes)
	if err != nil {
		return nil, err
	}
	c.buckets, c.at = buckets, now
	return buckets, nil
}

// countDecisions counts the decision records from since on into n buckets
// of length step, oldest first, and groups the calls policy stopped among
// them. It pages the window in batches and decodes each record's type, time
// and decision keys; a record that does not decode, is not a decision, or
// falls outside the window is skipped, because a dashboard must render what
// it can read rather than go dark on one bad row.
func (a *App) countDecisions(ctx context.Context, since time.Time, step time.Duration, n int) ([]decisionBucketPayload, map[string]*stoppedGroup, error) {
	buckets := make([]decisionBucketPayload, n)
	for i := range buckets {
		buckets[i].Start = since.Add(time.Duration(i) * step)
	}
	groups := map[string]*stoppedGroup{}
	// A held call is two records: the approval request, then the decision
	// record the lane spools once the hold resolves, keyed by the same
	// session and rule. The request arrives first in chain order, so the
	// pending map pairs them in one pass.
	pending := map[string][]pendingRequest{}
	window := a.approvalMatchWindow()
	for afterSeq := int64(0); ; {
		recs, err := a.store.Audit().ListSince(ctx, since, afterSeq, decisionScanBatch)
		if err != nil {
			return nil, nil, err
		}
		for _, rec := range recs {
			afterSeq = rec.Seq
			var ce decisionCE
			if json.Unmarshal([]byte(rec.CE), &ce) != nil {
				continue
			}
			i := int(ce.Time.UTC().Sub(since) / step)
			if i < 0 || i >= n {
				continue
			}
			if ce.Type == "straza.audit.approval" {
				if ce.Data.Phase == "request" {
					key := ce.Data.Session + "\x00" + ce.Data.Rule
					pending[key] = append(pending[key], pendingRequest{at: ce.Time.UTC(), summary: ce.Data.Summary})
				}
				continue
			}
			if ce.Type != "straza.audit.tool" && ce.Type != "straza.audit.mcp" {
				continue
			}
			outcome := decisionOutcome(ce.Data.Effect)
			if outcome == outcomeAllowed || outcome == outcomeDenied {
				if takePending(pending, ce.Data.Session+"\x00"+ce.Data.RuleID, ce.Time.UTC(), window) != nil {
					outcome = outcomeApproval
				}
			}
			switch outcome {
			case outcomeAllowed:
				if catalogRead(ce) {
					buckets[i].Catalog++
				} else {
					buckets[i].Allowed++
				}
			case outcomeApproval:
				buckets[i].Approval++
				tallyStopped(groups, outcomeApproval, ce)
			case outcomeDenied:
				buckets[i].Denied++
				tallyStopped(groups, outcomeDenied, ce)
			}
		}
		if len(recs) < decisionScanBatch {
			break
		}
	}
	// A request no decision record answered inside the window is still a
	// held call; it counts by its own time, named by its summary.
	for _, reqs := range pending {
		for _, r := range reqs {
			i := int(r.at.Sub(since) / step)
			if i < 0 || i >= n {
				continue
			}
			buckets[i].Approval++
			var ce decisionCE
			ce.Data.Tool = r.summary
			tallyStopped(groups, outcomeApproval, ce)
		}
	}
	return buckets, groups, nil
}

// tallyStopped counts one stopped call into its (app, tool, outcome) group
// and remembers the reason sentence it carried. A hook-lane record has no
// app, so the empty name is a group of its own rather than a dropped row.
func tallyStopped(groups map[string]*stoppedGroup, outcome string, ce decisionCE) {
	tool := ce.Data.Tool
	if ce.Data.ToolName != "" {
		tool = ce.Data.ToolName
	}
	key := ce.Data.App + "\x00" + tool + "\x00" + outcome
	g := groups[key]
	if g == nil {
		g = &stoppedGroup{app: ce.Data.App, tool: tool, outcome: outcome, reasons: map[string]int{}}
		groups[key] = g
	}
	g.count++
	g.reasons[ce.Data.Reason]++
}

// topStopped renders the busiest decisionStoppedMax groups, count first and
// then app, tool and outcome, so the list is stable between two reads of the
// same window. Each entry quotes the reason sentence that group carried most
// often, verbatim, because the operator acts on the words the record stored.
func topStopped(groups map[string]*stoppedGroup) []stoppedCallPayload {
	out := make([]stoppedCallPayload, 0, len(groups))
	for _, g := range groups {
		out = append(out, stoppedCallPayload{
			App: g.app, Tool: g.tool, Outcome: g.outcome, Count: g.count, Reason: topReason(g.reasons),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].App != out[j].App {
			return out[i].App < out[j].App
		}
		if out[i].Tool != out[j].Tool {
			return out[i].Tool < out[j].Tool
		}
		return out[i].Outcome < out[j].Outcome
	})
	if len(out) > decisionStoppedMax {
		out = out[:decisionStoppedMax]
	}
	return out
}

// topReason is the most frequent sentence of a group, the alphabetically
// first one when two sentences tie.
func topReason(reasons map[string]int) string {
	best, bestN := "", 0
	for reason, n := range reasons {
		if n > bestN || (n == bestN && reason < best) {
			best, bestN = reason, n
		}
	}
	return best
}
