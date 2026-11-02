package objects

import (
	"encoding/json"
	"slices"
	"sync/atomic"
	"unicode/utf8"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// Scope is a binding's covers line compiled for the gate: every object, or
// a set of object ids. A nil Scope covers nothing. It is safe for
// concurrent use.
type Scope struct {
	every bool
	ids   map[string]struct{}
	order []string

	// at caches the list indexes of order for the last list a refusal
	// named covered objects from, so a refusal costs no id lookups while
	// the list stays the same.
	at atomic.Pointer[scopeIndex]
}

// scopeIndex is the list indexes of a scope's ids in one list.
type scopeIndex struct {
	list *List
	at   []int
}

// CompileScope compiles a stored covers line for the gate. Nil in gives nil
// out, which covers nothing on a pinned server.
func CompileScope(s *store.ObjectScope) *Scope {
	if s == nil {
		return nil
	}
	c := &Scope{every: s.Every}
	if !s.Every {
		c.ids = make(map[string]struct{}, len(s.Objects))
		for _, o := range s.Objects {
			c.ids[o.ID] = struct{}{}
			c.order = append(c.order, o.ID)
		}
	}
	return c
}

// indexes answers the indexes in l of the objects the scope names that l
// holds.
func (s *Scope) indexes(l *List) []int {
	if x := s.at.Load(); x != nil && x.list == l {
		return x.at
	}
	x := &scopeIndex{list: l}
	for _, id := range s.order {
		if i, ok := l.byID[id]; ok {
			x.at = append(x.at, i)
		}
	}
	s.at.Store(x)
	return x.at
}

// Every reports whether the scope covers every object, including objects
// the server lists later.
func (s *Scope) Every() bool { return s != nil && s.every }

// Covers reports whether the scope covers the object whose list id is id.
func (s *Scope) Covers(id string) bool {
	if s == nil {
		return false
	}
	if s.every {
		return true
	}
	_, ok := s.ids[id]
	return ok
}

// partial reports whether the scope names objects one by one.
func (s *Scope) partial() bool { return s != nil && !s.every }

// Holding is one held binding that binds the called tool.
type Holding struct {
	Role, BindingID string
	Scope           *Scope
}

// Call is everything the gate decides a tools/call on. Held is sorted by
// role name. List is the replica's object list, nil before the first load.
// Enabled mirrors apps.objects.enabled. Args are the raw params.arguments.
type Call struct {
	Server, Type, Plural string
	Tool                 string
	Reach                manager.ToolReach
	Drifted              bool
	Enabled              bool
	Args                 json.RawMessage
	Held                 []Holding
	List                 *List
}

// Verdict is the gate's answer. Refusal is the sentence the agent reads,
// empty when the call may go on, and then Grant is the holding that admits
// it. SendID asks the caller to set ai.straza/object-id to Object.ID.
// Missed asks the caller to call Lists.Missed. Checked names, sorted, the
// held roles that bind the tool, for the record of a refusal.
type Verdict struct {
	Object  *Hit
	Grant   *Holding
	SendID  bool
	Refusal string
	Missed  bool
	Checked []string
}

// Hit is the object a call names, as the audit record carries it. Value is
// empty when the call's arguments did not yield one, and ID is empty when
// the list does not hold the value.
type Hit struct{ Type, Reach, Value, ID string }

// Decide answers whether the call may go on, and through which holding. It
// reads only its argument, so every replica decides alike. The caller skips
// it for a server that pins no type. The holdings are tried in their order
// and the first that covers the call grants it.
func Decide(c Call) Verdict {
	switch {
	case c.Reach.Reach == manager.ReachArgument:
		return c.decideArgument()
	case c.Reach.Reach == manager.ReachNone && !c.Drifted:
		return c.grant(nil, func(*Scope) bool { return true }, false, c.noRole)
	}
	// A drifted none, all, credential until its own check exists, and a
	// tool the pinned block does not name: only a role on every object
	// grants. The record names the object for credential and on a refusal.
	refusal := c.reachesEvery
	if c.Reach.Reach == manager.ReachNone {
		refusal = c.drifted
	}
	v := c.grant(&Hit{Type: c.Type, Reach: c.Reach.Reach}, (*Scope).Every, false, refusal)
	if v.Refusal == "" && c.Reach.Reach != manager.ReachCredential {
		v.Object = nil
	}
	return v
}

// decideArgument decides a tool whose reach is argument: the argument
// checks for every caller, then a role on every object, or a partial role
// that covers the object while the checks are on and the tool has not
// drifted.
func (c Call) decideArgument() Verdict {
	hit := &Hit{Type: c.Type, Reach: manager.ReachArgument}
	value, refusal := ArgValue(c.Tool, c.Server, c.Type, c.Reach.Argument, c.Args)
	if refusal != "" {
		return Verdict{Object: hit, Refusal: refusal, Checked: c.checked()}
	}
	hit.Value = value
	if c.List != nil {
		if o, ok := c.List.ByValue(value); ok {
			hit.ID = o.ID
		}
	}
	partial := c.Enabled && !c.Drifted && hit.ID != ""
	covers := func(s *Scope) bool { return s.Every() || (partial && s.Covers(hit.ID)) }
	switch {
	case !c.Enabled:
		return c.grant(hit, covers, false, c.checksOff)
	case c.Drifted && c.hasPartial():
		return c.grant(hit, covers, false, c.drifted)
	}
	v := c.grant(hit, covers, true, func() string { return c.uncovered(value) })
	v.Missed = v.Refusal != "" && hit.ID == ""
	return v
}

// grant answers the first holding whose scope covers admits, with the id
// sent when sendID is set and that holding is partial, or the sentence
// refusal builds with the roles checked when none does. refusal runs only
// on a refusal, because the uncovered sentence walks every covered id.
func (c Call) grant(hit *Hit, covers func(*Scope) bool, sendID bool, refusal func() string) Verdict {
	for i := range c.Held {
		if covers(c.Held[i].Scope) {
			return Verdict{Object: hit, Grant: &c.Held[i], SendID: sendID && c.Held[i].Scope.partial()}
		}
	}
	return Verdict{Object: hit, Refusal: refusal(), Checked: c.checked()}
}

// hasPartial reports whether a holding names objects one by one.
func (c Call) hasPartial() bool {
	for _, h := range c.Held {
		if h.Scope.partial() {
			return true
		}
	}
	return false
}

// checked names, sorted and once each, the roles of the holdings.
func (c Call) checked() []string {
	var out []string
	for _, h := range c.Held {
		out = append(out, h.Role)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Cut answers v cut to at most max bytes on a rune boundary, and whether
// it cut anything.
func Cut(v string, max int) (string, bool) {
	if len(v) <= max {
		return v, false
	}
	for max > 0 && !utf8.RuneStart(v[max]) {
		max--
	}
	return v[:max], true
}
