package server

import (
	"testing"

	"github.com/strazahq/straza/internal/drafts"
)

// TestAcksRefusalSaysWhatToType pins what step 6 asks the publisher to
// type for the first line left: a typed line of a group with two or more
// typed lines asks for the group's count, which the console has the
// publisher type once for the group and strazactl takes in --confirm, while a typed line alone in its
// group and a line confirmed by its own name, such as a server removal,
// ask for that name, a tick line asks to confirm it, and a wrong text names
// the text the line takes.
func TestAcksRefusalSaysWhatToType(t *testing.T) {
	t.Parallel()
	typed := func(code, group, object, name string) drafts.Finding {
		return drafts.Finding{Code: code, Class: drafts.ClassRisk, Ack: drafts.AckTyped, Object: object, Typed: name, Group: group,
			Sentence: name + " stops gating the holders of tracker-triage."}
	}
	desk := typed("policy.guardrail-changed", "policy.guardrail-changed", "PolicySet/desk-hold", "desk-hold")
	queue := typed("policy.guardrail-changed", "policy.guardrail-changed", "PolicySet/queue-hold", "queue-hold")
	host := typed("server.credential-host", "server.credential-host", "App/tracker", "tickets.example.net")
	removal := typed("server.removal", "", "App/helpdesk", "helpdesk")
	loosen := typed("agent.guardrail", "", "PolicySet/desk-gate", "desk-gate")
	tick := drafts.Finding{Code: "role.covers-wider", Class: drafts.ClassRisk, Ack: drafts.AckTick, Object: "Role/tracker-triage", Group: "role.covers-wider",
		Sentence: "tracker-triage will reach 1 more ticket on tracker: T-2."}
	v := drafts.Verdict{Risks: []drafts.Finding{desk, queue, host, removal, loosen, tick}}
	view := verdictPayload{Risks: keyed(v.Risks, nil, nil)}
	// body confirms every line but the one with the key left, which keeps
	// its tick when ticked is true, with text as the text typed for it.
	body := func(left string, ticked bool, text string) publishBody {
		b := publishBody{Typed: map[string]string{}}
		for _, f := range v.Risks {
			if f.Key() != left || ticked {
				b.Ticked = append(b.Ticked, f.Key())
			}
			if typedRisk(f) && f.Key() != left {
				b.Typed[f.Key()] = f.Typed
			}
		}
		if text != "" {
			b.Typed[left] = text
		}
		return b
	}
	cases := []struct {
		name string
		body publishBody
		want string
	}{
		{"every line confirmed", body("", false, ""), ""},
		{"a grouped line with neither tick nor text", body(queue.Key(), false, ""),
			"Publish refused: 2 lines like this one are not all confirmed: queue-hold stops gating the holders of tracker-triage. " +
				"Type 2 to confirm them when you publish, then publish again."},
		{"a grouped line ticked with no text", body(desk.Key(), true, ""),
			"Publish refused: 2 lines like this one are not all confirmed: desk-hold stops gating the holders of tracker-triage. " +
				"Type 2 to confirm them when you publish, then publish again."},
		{"a typed line alone in its group", body(host.Key(), false, ""),
			"Publish refused: tickets.example.net stops gating the holders of tracker-triage. Type tickets.example.net to confirm, then publish again."},
		{"a server removal", body(removal.Key(), false, ""),
			"Publish refused: helpdesk stops gating the holders of tracker-triage. Type helpdesk to confirm, then publish again."},
		{"an agent's loosening", body(loosen.Key(), false, ""),
			"Publish refused: desk-gate stops gating the holders of tracker-triage. Type desk-gate to accept it, then publish again."},
		{"a wrong text of an agent's loosening", body(loosen.Key(), true, "desk-gat"),
			"Publish refused: the text typed for PolicySet/desk-gate is not desk-gate. Type desk-gate to accept it, then publish again."},
		{"a tick line", body(tick.Key(), false, ""),
			"Publish refused: tracker-triage will reach 1 more ticket on tracker: T-2. Confirm it, then publish again."},
		{"a wrong text of a grouped line", body(desk.Key(), true, "desk-hld"),
			"Publish refused: the text typed for PolicySet/desk-hold is not desk-hold. Type desk-hold to confirm, then publish again."},
	}
	for _, tc := range cases {
		if got := acksRefusal("57", v, view, tc.body); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}
