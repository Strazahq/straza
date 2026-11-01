package agentguard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/strazahq/straza/internal/policy"
)

// TestNativeToolRidesBesideTheEvent pins both client hops of a classified
// tool's own name: the hook hands it to the service beside the event, and
// the service's call to strazad sends it beside the event too, because the
// event's JSON leaves it out.
func TestNativeToolRidesBesideTheEvent(t *testing.T) {
	svc := newFakeService(t)
	d := serviceDecider{store: svc.store(t), svc: NewServiceClient()}
	d.Decide(Normalized{Event: policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolTaskSpawn},
		HarnessName: "claude-code", SessionID: "w1", NativeTool: "SubagentHandback"})
	if got := svc.decided(); len(got) != 1 || got[0].NativeTool != "SubagentHandback" {
		t.Fatalf("hook to service requests = %+v, want one carrying SubagentHandback", got)
	}

	var body map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"effect":"allow"}`))
	}))
	defer srv.Close()
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolTaskSpawn, NativeTool: "SubagentHandback"}
	if _, err := NewClient(srv.URL).Decide(context.Background(), "tok", ev, "", ""); err != nil {
		t.Fatal(err)
	}
	var inEvent map[string]any
	if err := json.Unmarshal(body["event"], &inEvent); err != nil {
		t.Fatal(err)
	}
	if string(body["nativeTool"]) != `"SubagentHandback"` || inEvent["nativeTool"] != nil {
		t.Errorf("service to strazad body = %s, want nativeTool beside the event and not in it", body)
	}
}
