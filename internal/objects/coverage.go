package objects

import "github.com/strazahq/straza/internal/manager"

// Coverage is what a call's holdings let its tool touch, read without
// arguments. Named is false for a tool that names no object, and the other
// fields are then zero. Every is set when a call may touch any object of
// the type. Otherwise First holds up to coveredMax values of the covered
// objects the list holds, in list order, and Count says how many there
// are, so a zero Count means no call of the tool passes the gate.
type Coverage struct {
	Named bool
	Every bool
	First []string
	Count int
}

// Covers answers the Coverage of c's tool by the rules Decide applies. A
// tool whose reach is none and has not drifted names no object. A role on
// every object covers any call. A role on some objects covers its listed
// objects only on a tool whose reach is argument, while the checks are on
// and the tool has not drifted. c.Args is not read.
func (c Call) Covers() Coverage {
	if c.Reach.Reach == manager.ReachNone && !c.Drifted {
		return Coverage{}
	}
	out := Coverage{Named: true}
	for _, h := range c.Held {
		if h.Scope.Every() {
			out.Every = true
			return out
		}
	}
	if c.Reach.Reach == manager.ReachArgument && c.Enabled && !c.Drifted {
		out.First, out.Count = c.covered()
	}
	return out
}
