package drafts

import (
	"cmp"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// codeCoversWider is the risk of a held role whose covers line comes to
// reach more objects of a server that pins an object type.
const codeCoversWider = "role.covers-wider"

// coverage answers the Objects of the verdict: what each role covers, now
// and after publishing, on each server that pins an object type in either
// world, for every role the draft puts or removes with a row on that
// server, and for every role with a row on a server whose pin the draft
// changes. The holders are counted as who gains counts them. It is sorted
// by server, then role, and empty rather than nil.
func (c *check) coverage() []Coverage {
	named := map[string]bool{}
	for _, it := range c.d.Items {
		if it.Kind == KindRole {
			named[it.Name] = true
		}
	}
	out := []Coverage{}
	for _, r := range roleNames(c.w, c.after) {
		var servers []string
		for _, acc := range []Access{c.w.Access[r], c.after.Access[r]} {
			if acc.Server != "" && !slices.Contains(servers, acc.Server) {
				servers = append(servers, acc.Server)
			}
		}
		for _, s := range servers {
			live, inW := c.w.Apps[s]
			next, inA := c.after.Apps[s]
			// An unpin leaves the type to the live pin.
			pin := cmp.Or(next.Pinned, live.Pinned)
			repinned := inW && inA && live.Pinned.reachKey() != next.Pinned.reachKey()
			if pin == nil || (!named[r] && !repinned) {
				continue
			}
			holders := c.holdersOf(r)
			cv := Coverage{Role: r, Server: s, Type: pin.Type, Plural: pin.Plural, Holders: holders, HolderCount: len(holders)}
			cv.EveryBefore, cv.Before = rowCoverage(c.w, r, s)
			cv.EveryAfter, cv.After = rowCoverage(c.after, r, s)
			out = append(out, cv)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Server != out[j].Server {
			return out[i].Server < out[j].Server
		}
		return out[i].Role < out[j].Role
	})
	return out
}

// rowCoverage answers what the access row of role in w covers on server,
// read as objectGranted reads a row: every object on a server w pins no
// type on, through a row with no covers line on a server the draft pins
// first, and through a covers line on every object. Otherwise objects
// holds what the covers line names, sorted by value, and a role with no
// row on server covers none. objects is empty rather than nil.
func rowCoverage(w World, role, server string) (every bool, objects []ObjectName) {
	objects = []ObjectName{}
	acc, ok := w.Access[role]
	if !ok || acc.Server != server {
		return false, objects
	}
	p := w.Apps[server].Pinned
	switch {
	case p == nil, acc.Covers == nil && p.LiveType == "", acc.Covers != nil && acc.Covers.Every:
		return true, objects
	case acc.Covers == nil:
		return false, objects
	}
	for _, o := range acc.Covers.Objects {
		objects = append(objects, ObjectName{ID: o.ID, Value: o.Value})
	}
	sort.SliceStable(objects, func(i, j int) bool { return objects[i].Value < objects[j].Value })
	return false, objects
}

// coversRisks answers role.covers-wider, a tick, for each entry of cov
// whose role someone holds and that did not cover every object and comes
// to cover every object, or an object id it did not cover, so a renamed
// object is no change. The sentence names the objects it adds and never a
// holder or a count, which the client reads from cov. Before and After are
// the values covered now and after, or every and the type.
func coversRisks(cov []Coverage) []Finding {
	var out []Finding
	for _, cv := range cov {
		if cv.HolderCount == 0 || cv.EveryBefore {
			continue
		}
		var added []string
		for _, o := range cv.After {
			if !slices.ContainsFunc(cv.Before, func(b ObjectName) bool { return b.ID == o.ID }) {
				added = append(added, o.Value)
			}
		}
		sentence := fmt.Sprintf("%s will reach more %s on %s: %s.", cv.Role, cv.Plural, cv.Server, listWords(added, "and"))
		switch {
		case cv.EveryAfter:
			sentence = fmt.Sprintf("%s will reach every %s on %s, including %s added later.", cv.Role, cv.Type, cv.Server, cv.Plural)
		case len(added) == 0:
			continue
		}
		out = append(out, risk(codeCoversWider, "Role/"+cv.Role, "", sentence, coverWords(cv.EveryBefore, cv.Before, cv.Type), coverWords(cv.EveryAfter, cv.After, cv.Type)))
	}
	return out
}

// coverWords spells what a role covers for the words of a risk: every and
// the type, or the values joined by ", ".
func coverWords(every bool, objects []ObjectName, typ string) string {
	if every {
		return "every " + typ
	}
	values := make([]string, len(objects))
	for i, o := range objects {
		values[i] = o.Value
	}
	return strings.Join(values, ", ")
}
