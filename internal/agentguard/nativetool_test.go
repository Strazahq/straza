package agentguard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/policy"
)

// TestNativeToolRidesBesideTheEvent pins both client hops of a classified
// tool's own name and of its input: the hook hands them to the service
// beside the event, and the service's call to strazad sends them beside the
// event too, because the event's JSON leaves them out.
func TestNativeToolRidesBesideTheEvent(t *testing.T) {
	svc := newFakeService(t)
	d := serviceDecider{store: svc.store(t), svc: NewServiceClient()}
	in := &localapi.ToolInput{Text: `{"prompt":"sweep"}`, Hash: "sha256:ab"}
	d.Decide(Normalized{Event: policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolTaskSpawn},
		HarnessName: "claude-code", SessionID: "w1", NativeTool: "SubagentHandback", ToolInput: in})
	if got := svc.decided(); len(got) != 1 || got[0].NativeTool != "SubagentHandback" || got[0].ToolInput == nil || *got[0].ToolInput != *in {
		t.Fatalf("hook to service requests = %+v, want one carrying SubagentHandback and the input", got)
	}

	var body map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"effect":"allow"}`))
	}))
	defer srv.Close()
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolTaskSpawn, NativeTool: "SubagentHandback"}
	if _, err := NewClient(srv.URL).Decide(context.Background(), "tok", ev, "", "", in); err != nil {
		t.Fatal(err)
	}
	var inEvent map[string]any
	if err := json.Unmarshal(body["event"], &inEvent); err != nil {
		t.Fatal(err)
	}
	if string(body["nativeTool"]) != `"SubagentHandback"` || inEvent["nativeTool"] != nil {
		t.Errorf("service to strazad body = %s, want nativeTool beside the event and not in it", body)
	}
	if string(body["toolInput"]) != `{"text":"{\"prompt\":\"sweep\"}","hash":"sha256:ab"}` || inEvent["toolInput"] != nil || inEvent["args"] != nil {
		t.Errorf("service to strazad body = %s, want toolInput beside the event and not in it", body)
	}
	body = nil
	if _, err := NewClient(srv.URL).Decide(context.Background(), "tok", ev, "", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, has := body["toolInput"]; has {
		t.Errorf("service to strazad body = %s, want no toolInput when the hook sent none", body)
	}
}
