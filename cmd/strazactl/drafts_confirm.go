package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/strazahq/straza/internal/ctl"
)

// The kinds of a confirm block: one tick, one typed name, or one typed
// count over several typed lines of a group.
const (
	blockTick  = "tick"
	blockName  = "name"
	blockCount = "count"
)

// agentLoosenCode is the risk of an agent's draft that loosens a set binding
// that same agent, which a person accepts when they publish.
const agentLoosenCode = "agent.guardrail"

// confirmBlock is one thing a publisher confirms. lines are the risks it
// confirms in the verdict's order, expect is the text to type, empty for a
// tick, and blocked says a line's typed text was cut for this reader, so
// the block cannot be confirmed here.
type confirmBlock struct {
	kind    string
	lines   []ctl.DraftFinding
	expect  string
	blocked bool
}

// confirmBlocks groups the risks the way a publish asks for them, the rule
// the console's confirmBlocks keeps in web/ui/src/lib/drafts-model.ts, so a
// change goes in both. Typed lines with no group share a block per object
// and typed text. Typed lines of one group share a block, confirmed by
// their count when there are several and by the name when there is one.
// Tick lines of one group share one tick, and a tick line with no group
// stands alone, as every line of a server that sends no group does. Name
// blocks come first, then counts, then ticks, each in the order of its
// first line.
func confirmBlocks(risks []ctl.DraftFinding) []confirmBlock {
	at := map[string]int{}
	var blocks []confirmBlock
	add := func(id, kind string, f ctl.DraftFinding) {
		i, ok := at[id]
		if !ok {
			i = len(blocks)
			at[id] = i
			blocks = append(blocks, confirmBlock{kind: kind})
		}
		blocks[i].lines = append(blocks[i].lines, f)
	}
	for _, f := range risks {
		switch {
		case f.Ack != "typed" && f.Group != "":
			add("tick:"+f.Group, blockTick, f)
		case f.Ack != "typed":
			add("tick:"+f.Key, blockTick, f)
		case f.Group != "":
			add("typed:"+f.Group, blockCount, f)
		case f.Typed == "":
			add("cut:"+f.Key, blockName, f)
		default:
			add("name:"+f.Object+"\n"+strings.ToLower(strings.TrimSpace(f.Typed)), blockName, f)
		}
	}
	rank := map[string]int{blockName: 0, blockCount: 1, blockTick: 2}
	for i := range blocks {
		b := &blocks[i]
		for _, f := range b.lines {
			b.blocked = b.blocked || b.kind != blockTick && f.Typed == ""
		}
		if b.kind == blockCount && len(b.lines) == 1 {
			b.kind = blockName
		}
		switch b.kind {
		case blockCount:
			b.expect = strconv.Itoa(len(b.lines))
		case blockName:
			b.expect = b.lines[0].Typed
		}
	}
	sort.SliceStable(blocks, func(i, j int) bool { return rank[blocks[i].kind] < rank[blocks[j].kind] })
	return blocks
}

// loosens reports whether the block holds an agent's loosening of a set
// that binds it, which a person accepts rather than confirms.
func (b confirmBlock) loosens() bool {
	for _, f := range b.lines {
		if f.Code == agentLoosenCode {
			return true
		}
	}
	return false
}

// verb is what typing the block's text does: accept it for an agent's
// self-loosening, confirm for every other block.
func (b confirmBlock) verb() string {
	if b.loosens() {
		return "accept it"
	}
	return "confirm"
}

// hint says under a typed block's lines what a person types for it when
// they publish the draft in the console, once for the block, or why this
// reader cannot confirm it. A tick block has none.
func (b confirmBlock) hint() string {
	switch {
	case b.blocked:
		return "Only someone who can read what this line names can confirm it, so ask an admin who can read it to publish this draft."
	case b.kind == blockTick:
		return ""
	case b.kind == blockCount:
		return fmt.Sprintf("Type %s to confirm them when you publish the draft in the console under Drafts.", b.expect)
	}
	verb := b.verb()
	switch {
	case b.loosens():
	case len(b.lines) == 1:
		verb += " it"
	default:
		verb += " them"
	}
	return fmt.Sprintf("Type %s to %s when you publish the draft in the console under Drafts.", b.expect, verb)
}

// capFirst is s with its first letter in upper case.
func capFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
