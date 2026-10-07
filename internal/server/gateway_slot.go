package server

import (
	"maps"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// decisionSlotKey is the _meta key of the decision slot on a tools/call
// result.
const decisionSlotKey = "intermediary/decision"

// heldSlotReason is the slot's reason on every held result. A Straza hold
// never resumes the call on its own, so it tells the person to do the action
// again once it is approved.
const heldSlotReason = "This action waits for a person's approval. Once it is approved, do it again."

// slotReasonMax caps the slot's reason, in characters.
const slotReasonMax = 500

// decisionSlot is the value of _meta["intermediary/decision"] on a held or
// denied tools/call result of a server endpoint whose views are on. A view
// shows Reason, one plain sentence for a person, under the label "Reported
// by Straza". Audited says whether the call's audit record entered the
// queue. Ref names the approval when there is one, and ExpiresAt ends a held
// approval's decision window.
type decisionSlot struct {
	V         int    `json:"v"`
	Decision  string `json:"decision"`
	Audited   bool   `json:"audited"`
	Reason    string `json:"reason"`
	Ref       string `json:"ref,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	Source    string `json:"source"`
}

// heldSlot is the slot of a call whose approval ref is still undecided until
// expiresAt.
func heldSlot(ref string, expiresAt time.Time, audited bool) decisionSlot {
	s := decisionSlot{V: 1, Decision: "held", Audited: audited, Reason: heldSlotReason, Ref: ref, Source: "Straza"}
	if !expiresAt.IsZero() {
		s.ExpiresAt = expiresAt.UTC().Format(time.RFC3339)
	}
	return s
}

// deniedSlot is the slot of a refused call whose model-facing refusal is
// text. ref names the approval the refusal is about, or is empty.
func deniedSlot(text string, audited bool, ref string) decisionSlot {
	return decisionSlot{V: 1, Decision: "denied", Audited: audited, Reason: personReason(text), Ref: ref, Source: "Straza"}
}

// personReason turns a refusal the model reads into the slot's sentence for
// a person: the "Straza: " prefix dropped, since the view names the source
// itself, the first letter upper-cased, and the text cut to slotReasonMax
// characters with an ellipsis when it is longer.
func personReason(text string) string {
	s := strings.TrimSpace(text)
	s = strings.TrimSpace(strings.TrimPrefix(s, "Straza:"))
	if first, size := utf8.DecodeRuneInString(s); size > 0 {
		s = string(unicode.ToUpper(first)) + s[size:]
	}
	if utf8.RuneCountInString(s) <= slotReasonMax {
		return s
	}
	runes := []rune(s)
	return string(runes[:slotReasonMax-1]) + "…"
}

// toolRefusal is the tool-level error a refused tools/call answers with.
// text is the sentence the model reads, verbatim, and the result is the same
// on every endpoint, except that on a server endpoint whose views are on it
// also carries slot under decisionSlotKey for the view.
func toolRefusal(text string, srv *serverScope, slot decisionSlot) *mcp.CallToolResult {
	res := &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
	if srv.viewsOn() {
		res.Meta = mcp.Meta{decisionSlotKey: slot}
	}
	return res
}

// withoutUpstreamSlot is an upstream result as a server endpoint serves it:
// without the upstream's own decision slot, so only Straza speaks under that
// key and a call that ran cannot pass as a hold or a denial. It drops the key
// on a copy and never writes the upstream's Meta map.
func withoutUpstreamSlot(res *mcp.CallToolResult) *mcp.CallToolResult {
	if res == nil {
		return nil
	}
	if _, ok := res.Meta[decisionSlotKey]; !ok {
		return res
	}
	cp := *res
	cp.Meta = maps.Clone(res.Meta)
	delete(cp.Meta, decisionSlotKey)
	return &cp
}
