package server

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
)

// TestPublishRecordsAnAgentsOwnReach pins the publish of an agent's draft
// that widens a role the agent holds. The draft is stored open with
// agent.own-reach as a risk typed by the server's name. A publish without
// that name, or with another text, is refused in a sentence that says what
// to type, and the agent cannot publish at all. A person who types the
// name publishes it, and exactly one draft.publish record names that
// person as the actor, the agent as the proposer, and the risk's key under
// typed, never the typed text.
func TestPublishRecordsAnAgentsOwnReach(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	rob := seedAgent(t, f.app, "rob", "kim", DraftConfigRole)
	if _, _, err := f.app.store.Roles().CreateOwned(context.Background(), store.Role{Name: "jira-read", OwnerAppID: f.jira.ID}, `["get_issue"]`); err != nil {
		t.Fatal(err)
	}
	grantRole(t, f.app, rob.ID, "jira-read")
	id := f.agentDraft(t, rob, "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: jira-read\nspec:\n    kind: application\n    server: jira\n"+
		"    bindings:\n        - app: jira\n          tools: [get_issue, delete_issue]\n")
	rev, v := f.read(t, f.root, id)
	const sentence = "This draft lets the agent that wrote it reach tools of jira that nobody has listed yet."
	at := slices.IndexFunc(v.Risks, func(r pubFinding) bool { return r.Code == "agent.own-reach" })
	if at < 0 || len(v.Refused) > 0 {
		t.Fatalf("the agent's draft reads refused %+v, risks %+v, want it open with an agent.own-reach risk", v.Refused, v.Risks)
	}
	risk := v.Risks[at]
	if risk.Ack != "typed" || risk.Typed != "jira" || risk.Object != "App/jira" || risk.Sentence != sentence {
		t.Errorf("agent.own-reach = %+v, want it typed jira on App/jira with the sentence %q", risk, sentence)
	}
	row, _ := f.stored(t, id)
	if row.State != string(drafts.StateOpen) || row.Door != string(drafts.DoorAgent) || !row.Proposer.Agent {
		t.Errorf("the draft is %s through the door %s by %+v, want it open through the agent's door", row.State, row.Door, row.Proposer)
	}
	withTyped := func(text string) map[string]any {
		body := acksFor(rev, v)
		typed := body["typed"].(map[string]string)
		delete(typed, risk.Key)
		if text != "" {
			typed[risk.Key] = text
		}
		return body
	}
	for _, tc := range []struct{ name, typed, want string }{
		{"no typed name", "", "Publish refused: " + sentence + " Type jira to accept it, then publish again."},
		{"another text", "github", "Publish refused: the text typed for App/jira is not jira. Type jira to accept it, then publish again."},
	} {
		if code, a, _ := f.publish(t, f.root, id, withTyped(tc.typed)); code != http.StatusConflict || a.Error != tc.want {
			t.Errorf("a publish with %s = %d %q, want 409 %q", tc.name, code, a.Error, tc.want)
		}
	}
	want := fmt.Sprintf(nonPersonAdminRefusal, "rob", "an agent")
	if code, a, _ := f.publish(t, agentIDToken(t, f.app, "rob"), id, acksFor(rev, v)); code != http.StatusForbidden || a.Error != want {
		t.Errorf("the agent's own publish = %d %q, want 403 %q", code, a.Error, want)
	}
	if row, _ := f.stored(t, id); row.State != string(drafts.StateOpen) {
		t.Fatalf("the draft is %s after the refused publishes, want it open", row.State)
	}
	if code, a, _ := f.publish(t, f.root, id, withTyped(" JIRA ")); code != http.StatusOK {
		t.Fatalf("a person's publish with the name typed = %d %q", code, a.Error)
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
	typed, _ := p["typed"].([]any)
	if p["actor"] != "kim" || p["draft"] != id || p["proposer"] != "rob" || p["proposerVia"] != laneSession || !slices.Contains(typed, any(risk.Key)) {
		t.Errorf("draft.publish = %v, want the actor kim, the proposer rob through its session, and the key %s under typed", p, risk.Key)
	}
	if _, ok := p["agentLoosened"]; ok {
		t.Errorf("draft.publish = %v, want no agentLoosened for a draft that loosens no set", p)
	}
	if w, err := f.app.readWorld(context.Background()); err != nil {
		t.Fatal(err)
	} else if got := w.Access["jira-read"].Tools; !slices.Contains(got, "delete_issue") {
		t.Errorf("jira-read reaches %v after the publish, want delete_issue too", got)
	}
}
