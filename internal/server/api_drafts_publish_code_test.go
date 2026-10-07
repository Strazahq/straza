package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestPublishSecondPersonAnswersItsCode pins the code of the 409 that
// admin.secondPerson answers, so a client tells that refusal from any other
// 409 of the publish route: every sentence of the setting carries the code
// second_person, and a moved revision, a risk left unacknowledged and a
// caller short of standing carry none. The standing refusal comes first, so
// such a caller learns nothing of the setting.
func TestPublishSecondPersonAnswersItsCode(t *testing.T) {
	t.Parallel()
	f, lars, rob := secondPersonFixture(t)
	gone, goneID := f.mintToken(t, "drafter2", "drafts:read,drafts:write,apps:read")
	byKim := f.create(t, f.root, removalDoc("App", "srv-a")).Draft.ID
	byToken := f.create(t, f.token, removalDoc("App", "srv-b")).Draft.ID
	byGone := f.create(t, gone, removalDoc("App", "srv-c")).Draft.ID
	if code := adminReq(t, http.MethodDelete, f.base+"/v1/admin/api-tokens/"+goneID, f.root, nil, nil); code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("revoke drafter2 = %d", code)
	}
	byRob := f.agentDraft(t, rob, removalDoc("App", "srv-d"))
	acked := func(id string) map[string]any {
		rev, v := f.read(t, f.root, id)
		return acksFor(rev, v)
	}
	moved := acked(byKim)
	moved["revision"] = moved["revision"].(int) + 1
	unacked := acked(byKim)
	unacked["ticked"], unacked["typed"] = []string{}, map[string]string{}
	for _, tc := range []struct {
		name, bearer, id string
		body             map[string]any
		status           int
		code             string
	}{
		{"its author", f.root, byKim, acked(byKim), http.StatusConflict, codeSecondPerson},
		{"the minter of the token that wrote it", f.root, byToken, acked(byToken), http.StatusConflict, codeSecondPerson},
		{"anyone, when the token's minter cannot be traced", lars, byGone, acked(byGone), http.StatusConflict, codeSecondPerson},
		{"the sponsor of the agent that wrote it", f.root, byRob, acked(byRob), http.StatusConflict, codeSecondPerson},
		{"a revision that moved", lars, byKim, moved, http.StatusConflict, ""},
		{"a risk left unacknowledged", lars, byKim, unacked, http.StatusConflict, ""},
		{"a person short of standing", f.ada, byKim, acked(byKim), http.StatusForbidden, ""},
	} {
		status, _, out := f.publish(t, tc.bearer, tc.id, tc.body)
		var answer map[string]any
		if err := json.Unmarshal(out, &answer); err != nil {
			t.Fatalf("%s: %v in %s", tc.name, err, out)
		}
		code, has := answer["code"]
		if status != tc.status || (tc.code == "" && has) || (tc.code != "" && code != tc.code) {
			t.Errorf("%s = %d %s, want %d with code %q", tc.name, status, out, tc.status, tc.code)
		}
	}
}
