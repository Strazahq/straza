package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
)

// bindGuard publishes guard, the set that denies shell calls to dev, as
// kim's draft that the bearer publisher publishes, and gives agent the role
// dev, so guard binds the agent.
func (f *draftsFixture) bindGuard(t *testing.T, agent store.User, publisher string) {
	t.Helper()
	if code, a := f.publishAll(t, publisher, f.create(t, f.root, draftSet("guard")).Draft.ID); code != http.StatusOK {
		t.Fatalf("publish guard = %d %q", code, a.Error)
	}
	grantRole(t, f.app, agent.ID, "dev")
}

// agentDirectRequest is a request to a direct route as agent, with the
// actor and the full standing a route's wrapper puts in the context, as if
// the agent's request had passed the admin API's refusal of anyone who is
// not a person.
func agentDirectRequest(agent store.User) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/admin/policy/sets/guard/status", nil)
	ctx := withActor(context.Background(), auditActor{Name: agent.Username, ID: agent.ID, Via: laneLogin})
	return r.WithContext(withStanding(ctx, adminStanding{Full: true}))
}

// TestDirectRouteRefusesAnAgentsLoosening pins the direct check beneath
// the admin API's refusal of an agent (TestAgentNeverReachesADirectRoute):
// a direct route that would turn off guard for an agent that guard binds,
// even one holding the root role, answers agent.guardrail's refusal and
// publishes nothing, both with admin.secondPerson off, where the route
// checks the refusals alone, and on.
func TestDirectRouteRefusesAnAgentsLoosening(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		second bool
	}{{"admin.secondPerson off", false}, {"admin.secondPerson on", true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newDraftsFixture(t, nil, func(c *config.Config) { c.Admin.SecondPerson = tc.second })
			mkHuman(t, f.app, "lars", AdminRole)
			bot, err := f.app.store.Users().GetByUsername(context.Background(), "bot")
			if err != nil {
				t.Fatal(err)
			}
			f.bindGuard(t, bot, f.login(t, "lars"))
			grantAdmin(t, f.app, bot.ID)
			before := f.drafts(t)
			var authors []drafts.Principal
			rec := httptest.NewRecorder()
			off := drafts.Item{Kind: drafts.KindPolicySet, Name: "guard", Op: drafts.OpOff, Doc: draftSet("guard")}
			_, ok := f.app.publishOne(rec, agentDirectRequest(bot), itemRoute(off, &authors))
			var answer struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &answer)
			if ok || rec.Code != http.StatusConflict || !strings.Contains(answer.Error, "guard applies to you") {
				t.Errorf("the agent's turn-off = %v %d %q, want 409 with agent.guardrail's refusal", ok, rec.Code, answer.Error)
			}
			if len(authors) == 0 || !authors[0].Agent {
				t.Errorf("the route read the authors %+v, want the agent", authors)
			}
			if n := f.drafts(t); n != before {
				t.Errorf("the refused turn-off stored %d drafts, want none", n-before)
			}
			if w, err := f.app.readWorld(context.Background()); err != nil {
				t.Fatal(err)
			} else if _, on := w.Policies["guard"]; !on {
				t.Error("guard is off after the refused turn-off")
			}
		})
	}
}

// TestPublishRecordsAnAgentsLoosening pins the publish of an agent's draft
// that removes guard, a set that binds the agent: a person who types
// guard's name publishes it, and exactly one draft.publish record names
// that person, the draft and agentLoosened [guard], with the fields of its
// spec/events example, while the publish of guard itself, a person's draft
// with no such risk, writes no agentLoosened. With admin.secondPerson on,
// the agent's sponsor is refused and another administrator publishes it.
func TestPublishRecordsAnAgentsLoosening(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		second bool
		actor  string
	}{{"admin.secondPerson off, the sponsor publishes", false, "kim"}, {"admin.secondPerson on, another administrator publishes", true, "lars"}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newDraftsFixture(t, nil, func(c *config.Config) { c.Admin.SecondPerson = tc.second })
			mkHuman(t, f.app, "lars", AdminRole)
			lars := f.login(t, "lars")
			rob := seedAgent(t, f.app, "rob", "kim", DraftConfigRole)
			f.bindGuard(t, rob, lars)
			plain := f.publishRecords(t)
			if len(plain) == 0 {
				t.Fatal("the publish of guard wrote no draft.publish record")
			}
			for _, ev := range plain {
				if _, ok := ev["agentLoosened"]; ok {
					t.Errorf("the publish of a person's draft records %v", ev)
				}
			}
			id := f.agentDraft(t, rob, removalDoc("PolicySet", "guard"))
			rev, v := f.read(t, f.root, id)
			loosened := false
			for _, r := range v.Risks {
				loosened = loosened || r.Code == "agent.guardrail" && r.Ack == "typed" && r.Typed == "guard"
			}
			if !loosened || len(v.Refused) > 0 {
				t.Fatalf("the agent's draft reads refused %+v, risks %+v, want an agent.guardrail risk typed guard", v.Refused, v.Risks)
			}
			publisher := f.root
			if tc.second {
				var refused struct {
					Code  string `json:"code"`
					Error string `json:"error"`
				}
				code, _, raw := f.publish(t, f.root, id, acksFor(rev, v))
				_ = json.Unmarshal(raw, &refused)
				if want := fmt.Sprintf(secondPersonSponsorRefusal, "rob"); code != http.StatusConflict || refused.Code != codeSecondPerson || refused.Error != want {
					t.Errorf("the sponsor's publish = %d %s, want 409 %s %q", code, raw, codeSecondPerson, want)
				}
				publisher = lars
			}
			if code, a, _ := f.publish(t, publisher, id, acksFor(rev, v)); code != http.StatusOK {
				t.Fatalf("publish = %d %q", code, a.Error)
			}
			var records []map[string]any
			for _, ev := range outboxOf(t, f.app, id) {
				if ev.data["action"] == "draft.publish" {
					records = append(records, ev.data)
				}
			}
			if len(records) != 1 {
				t.Fatalf("the publish wrote %d draft.publish records, want 1: %v", len(records), records)
			}
			p := records[0]
			if p["actor"] != tc.actor || p["draft"] != id || !reflect.DeepEqual(p["agentLoosened"], []any{"guard"}) {
				t.Errorf("draft.publish = %v, want %s, draft %s and agentLoosened [guard]", p, tc.actor, id)
			}
			if got, want := fieldNames(p), exampleFields(t, "valid-audit-admin-draft-publish-agent-loosened.json"); got != want {
				t.Errorf("draft.publish fields = %s, want the example's %s", got, want)
			}
		})
	}
}

// publishRecords answers the data of every draft.publish record the outbox
// holds.
func (f *draftsFixture) publishRecords(t *testing.T) []map[string]any {
	t.Helper()
	rows, err := f.app.store.Outbox().ListRecent(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, row := range rows {
		var ce struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(row.CE), &ce); err != nil {
			t.Fatal(err)
		}
		if ce.Data["action"] == "draft.publish" {
			out = append(out, ce.Data)
		}
	}
	return out
}
