package objects

import (
	"fmt"
	"math/bits"
	"strings"
)

// The sentences of the gate's refusals. The gateway puts "Straza: " in
// front of each. None quotes more than 200 bytes of the caller's value.

// echoMax is how many bytes of the caller's value a refusal echoes.
const echoMax = 200

// coveredMax is how many covered values the uncovered sentence lists.
const coveredMax = 10

// uncovered is the refusal of an object no held role covers, with the
// values the caller's partial roles cover that the list holds.
func (c Call) uncovered(value string) string {
	echo, cut := Cut(value, echoMax)
	if cut {
		echo += "..."
	}
	s := fmt.Sprintf("%s on %s was refused: none of your roles on %s covers that %s.", c.Tool, echo, c.Server, c.Type)
	if first, n := c.covered(); n > 0 {
		s += fmt.Sprintf(" Your roles cover %s on %s.", c.Tool, listWords(first, n))
	}
	return s + fmt.Sprintf(" Check the %s's name, or ask an admin for a role that covers it.", c.Type)
}

// covered answers the first coveredMax current values, in list order, of
// the objects the partial holdings cover that the list holds, and how many
// such objects there are. The list is sorted by value, so marking each
// covered object's index and reading the marks in order sorts them and
// counts each once, with no sort and no set as large as the list.
func (c Call) covered() ([]string, int) {
	if c.List == nil {
		return nil, 0
	}
	marks := make([]uint64, len(c.List.Objects)/64+1)
	n := 0
	for _, h := range c.Held {
		if !h.Scope.partial() {
			continue
		}
		for _, i := range h.Scope.indexes(c.List) {
			if marks[i/64]&(1<<(i%64)) == 0 {
				marks[i/64] |= 1 << (i % 64)
				n++
			}
		}
	}
	var first []string
	for w, word := range marks {
		for ; word != 0 && len(first) < coveredMax; word &= word - 1 {
			first = append(first, c.List.Objects[w*64+bits.TrailingZeros64(word)].Value)
		}
	}
	return first, n
}

// listWords joins the first values as a sentence lists them, then how many
// more of the n there are.
func listWords(first []string, n int) string {
	if n > len(first) {
		return strings.Join(first, ", ") + fmt.Sprintf(" and %d more", n-len(first))
	}
	if len(first) == 1 {
		return first[0]
	}
	return strings.Join(first[:len(first)-1], ", ") + " and " + first[len(first)-1]
}

// reachesEvery is the refusal of a tool that can reach every object.
func (c Call) reachesEvery() string {
	return fmt.Sprintf("%s on %s can reach every %s, so only a role on every %[3]s may call it, and none of your roles on %[2]s is one. Ask for a role on every %[3]s, or use a tool that names the %[3]s.",
		c.Tool, c.Server, c.Type)
}

// drifted is the refusal of a tool whose server now marks it stricter
// than the pinned reach.
func (c Call) drifted() string {
	plural := c.Plural
	if plural == "" {
		plural = c.Type + "s"
	}
	return fmt.Sprintf("%s on %s changed how it names its %s since an admin pinned it, so roles on some %s cannot use it until an admin publishes the change. Ask an admin to review %[2]s's %[5]s tab.",
		c.Tool, c.Server, c.Type, plural, strings.ToUpper(plural[:1])+plural[1:])
}

// checksOff is the refusal of a partial role while apps.objects.enabled
// is off.
func (c Call) checksOff() string {
	return fmt.Sprintf("Straza's %s checks are off on this deployment, so only a role on every %[1]s may call %s on %s. Ask an admin to turn apps.objects.enabled back on, or for a role on every %[1]s.",
		c.Type, c.Tool, c.Server)
}

// noRole is the refusal of a call no held role binds, which the gateway's
// catalog answers before the gate in practice.
func (c Call) noRole() string {
	return fmt.Sprintf("None of your roles binds %s on %s. Ask an admin for a role that does.", c.Tool, c.Server)
}
