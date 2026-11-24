package entragraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	// missingAgent is the made-up agent identity that does not exist.
	missingAgent = "3b3b3b3b-cccc-4ccc-8ccc-cccccccccccc"
	// userLee is the made-up sponsor of the recorded agent identity.
	userLee = "1c1c1c1c-7777-4777-8777-777777777777"
	// agentQuery follows the agent's ID in the address of each read.
	agentQuery = "/microsoft.graph.agentIdentity?$select=id,displayName,accountEnabled&$expand=sponsors($select=id)"
)

// reply is one response inside a $batch answer. A body that starts with @ is
// a file in testdata.
type reply struct {
	id     string
	status int
	header map[string]string
	body   string
}

// batchOf is the body of a $batch answer that holds replies in their order.
func batchOf(t *testing.T, replies ...reply) string {
	t.Helper()
	type response struct {
		ID      string            `json:"id"`
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    json.RawMessage   `json:"body"`
	}
	list := make([]response, len(replies))
	for i, r := range replies {
		body := r.body
		if name, ok := strings.CutPrefix(body, "@"); ok {
			body = load(t, name)
		}
		list[i] = response{ID: r.id, Status: r.status, Headers: r.header, Body: json.RawMessage(body)}
	}
	data, err := json.Marshal(map[string][]response{"responses": list})
	if err != nil {
		t.Fatalf("batchOf: %v", err)
	}
	return string(data)
}

// agentOf is the body of a read of the agent identity id with sponsors.
func agentOf(id string, sponsors ...string) string {
	return `{"id":"` + id + `","displayName":"agent","accountEnabled":true,"sponsors":[` + strings.Join(sponsors, ",") + `]}`
}

// sponsorOf is one sponsor of the type #microsoft.graph.typ.
func sponsorOf(typ, id string) string {
	return `{"@odata.type":"#microsoft.graph.` + typ + `","id":"` + id + `"}`
}

// agentIDs makes n distinct agent identity IDs.
func agentIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("3c3c3c3c-0000-4000-8000-%012d", i)
	}
	return ids
}

// batchStep is the script of one $batch request that Graph answers body.
func batchStep(body string) step {
	return step{method: http.MethodPost, path: "/v1.0/$batch", status: http.StatusOK, body: body}
}

func TestAgentsBatches(t *testing.T) {
	tests := []struct{ ids, batches int }{{0, 0}, {1, 1}, {20, 1}, {21, 2}, {45, 3}}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d ids", tt.ids), func(t *testing.T) {
			ids := agentIDs(tt.ids)
			var steps []step
			for start := 0; start < len(ids); start += 20 {
				var replies []reply
				for i, id := range ids[start:min(start+20, len(ids))] {
					replies = append(replies, reply{id: strconv.Itoa(i + 1), status: 200, body: agentOf(id)})
				}
				steps = append(steps, batchStep(batchOf(t, replies...)))
			}
			f := newFixture(t, steps...)
			got, err := f.client.Agents(context.Background(), ids)
			f.done()
			if err != nil || got == nil || len(got) != tt.ids {
				t.Fatalf("Agents = %d agents, %s, want %d", len(got), f.show(err), tt.ids)
			}
			if tt.ids == 0 && f.tokenRequests() != 0 {
				t.Errorf("token requests = %d, want none for no ID", f.tokenRequests())
			}
			asked := 0
			for n, r := range f.requests() {
				var body struct{ Requests []batchRequest }
				if err := json.Unmarshal(r.body, &body); err != nil || len(body.Requests) == 0 || len(body.Requests) > 20 {
					t.Fatalf("batch %d holds %d reads (%v), want 1 to 20", n+1, len(body.Requests), err)
				}
				if ct := r.header.Get("Content-Type"); ct != "application/json" {
					t.Errorf("batch %d Content-Type = %q", n+1, ct)
				}
				for i, in := range body.Requests {
					want := batchRequest{ID: strconv.Itoa(i + 1), Method: "GET", URL: "/servicePrincipals/" + ids[asked] + agentQuery}
					if in != want {
						t.Errorf("batch %d read %d = %+v, want %+v", n+1, i+1, in, want)
					}
					if a := got[ids[asked]]; a.ID != ids[asked] || a.Missing || !reflect.DeepEqual(a.Sponsors, []Sponsor{}) {
						t.Errorf("agent %s = %+v", ids[asked], a)
					}
					asked++
				}
			}
			if len(f.requests()) != tt.batches || asked != tt.ids {
				t.Errorf("sent %d batches of %d reads, want %d of %d", len(f.requests()), asked, tt.batches, tt.ids)
			}
		})
	}
}

func TestAgentsAnswers(t *testing.T) {
	ok := func(id, body string) reply { return reply{id: id, status: 200, body: body} }
	read := func(sponsors ...Sponsor) Agent {
		return Agent{ID: agentID, DisplayName: "agent", Enabled: on, Sponsors: append([]Sponsor{}, sponsors...)}
	}
	recorded := Agent{ID: agentID, DisplayName: "demo-agent", Enabled: on, Sponsors: []Sponsor{{ID: userLee, Kind: KindUser}}}
	internal := `{"error":{"code":"generalException","message":"General exception while processing."}}`
	ids3 := agentIDs(3)
	tests := []struct {
		name string
		ids  []string
		// bodies are the answers to the batches in order, each a $batch body.
		bodies []string
		want   map[string]Agent
		status int
		retry  time.Duration
		fail   []string
	}{
		{name: "the recorded read with one sponsor", ids: []string{agentID}, bodies: []string{batchOf(t, ok("1", "@agent-sponsors.json"))},
			want: map[string]Agent{agentID: recorded}},
		{name: "the recorded batch with a missing agent, out of order", ids: []string{agentID, missingAgent}, bodies: []string{"@agents-batch.json"},
			want: map[string]Agent{agentID: recorded, missingAgent: {ID: missingAgent, Missing: true}}},
		{name: "two sponsors", ids: []string{agentID}, bodies: []string{batchOf(t, ok("1", agentOf(agentID, sponsorOf("user", userPat), sponsorOf("user", userKim))))},
			want: map[string]Agent{agentID: read(Sponsor{userPat, KindUser}, Sponsor{userKim, KindUser})}},
		{name: "no sponsor", ids: []string{agentID}, bodies: []string{batchOf(t, ok("1", agentOf(agentID)))},
			want: map[string]Agent{agentID: read()}},
		{name: "a group as a sponsor", ids: []string{agentID}, bodies: []string{batchOf(t, ok("1", agentOf(agentID, sponsorOf("group", roleGroup))))},
			want: map[string]Agent{agentID: read(Sponsor{roleGroup, KindGroup})}},
		{name: "every kind of sponsor", ids: []string{agentID}, bodies: []string{batchOf(t, ok("1", agentOf(agentID,
			sponsorOf("agentUser", "s1"), sponsorOf("servicePrincipal", "s2"), sponsorOf("agentIdentity", "s3"), sponsorOf("device", "s4"))))},
			want: map[string]Agent{agentID: read(Sponsor{"s1", KindAgentUser}, Sponsor{"s2", KindServicePrincipal}, Sponsor{"s3", KindAgentIdentity}, Sponsor{"s4", KindOther})}},
		{name: "disabled, and enabled state left out", ids: ids3[:2], bodies: []string{batchOf(t,
			ok("1", `{"id":"`+ids3[0]+`","accountEnabled":false,"sponsors":[]}`), ok("2", `{"id":"`+ids3[1]+`","sponsors":[]}`))},
			want: map[string]Agent{ids3[0]: {ID: ids3[0], Enabled: off, Sponsors: []Sponsor{}}, ids3[1]: {ID: ids3[1], Sponsors: []Sponsor{}}}},
		{name: "answers in reverse order", ids: ids3, bodies: []string{batchOf(t, ok("3", agentOf(ids3[2])), ok("2", agentOf(ids3[1])), ok("1", agentOf(ids3[0])))},
			want: map[string]Agent{
				ids3[0]: {ID: ids3[0], DisplayName: "agent", Enabled: on, Sponsors: []Sponsor{}},
				ids3[1]: {ID: ids3[1], DisplayName: "agent", Enabled: on, Sponsors: []Sponsor{}},
				ids3[2]: {ID: ids3[2], DisplayName: "agent", Enabled: on, Sponsors: []Sponsor{}},
			}},
		{name: "one ID given twice is read once", ids: []string{agentID, agentID}, bodies: []string{batchOf(t, ok("1", agentOf(agentID)))},
			want: map[string]Agent{agentID: read()}},
		{name: "403", ids: []string{agentID}, bodies: []string{batchOf(t, reply{id: "1", status: 403, body: "@error-forbidden.json"})}, status: 403,
			fail: []string{"the read of agent identity " + agentID + " with HTTP 403 Authorization_RequestDenied", "Grant the service app the application permission AgentIdentity.Read.All and give admin consent."}},
		{name: "429 with Retry-After", ids: []string{agentID}, status: 429, retry: 9 * time.Second, fail: []string{"HTTP 429 TooManyRequests", "wait 9 seconds"},
			bodies: []string{batchOf(t, reply{id: "1", status: 429, header: map[string]string{"retry-after": "9"}, body: "@error-throttled.json"})}},
		{name: "500", ids: []string{agentID}, bodies: []string{batchOf(t, reply{id: "1", status: 500, body: internal})}, status: 500, fail: []string{"HTTP 500 generalException"}},
		{name: "500 in the second batch", ids: agentIDs(21), status: 500, fail: []string{"the read of agent identity " + agentIDs(21)[20]},
			bodies: []string{batchOfAll(t, agentIDs(20)), batchOf(t, reply{id: "1", status: 500, body: internal})}},
		{name: "a 204", ids: []string{agentID}, bodies: []string{batchOf(t, reply{id: "1", status: 204, body: "null"})}, fail: []string{"where Straza expects 200 or 404"}},
		{name: "a read left unanswered", ids: ids3[:2], bodies: []string{batchOf(t, ok("1", agentOf(ids3[0])))},
			fail: []string{"without an answer for agent identity " + ids3[1], "incomplete"}},
		{name: "an answer to no read", ids: []string{agentID}, bodies: []string{batchOf(t, ok("1", agentOf(agentID)), ok("2", agentOf(agentID)))},
			fail: []string{"a response to no read Straza sent"}},
		{name: "one read answered twice", ids: ids3[:2], bodies: []string{batchOf(t, ok("1", agentOf(ids3[0])), ok("1", agentOf(ids3[0])))},
			fail: []string{"or to one it answered before"}},
		{name: "an answer about another agent", ids: []string{agentID}, bodies: []string{batchOf(t, ok("1", agentOf(missingAgent)))},
			fail: []string{"without that agent identity and its list of sponsors"}},
		{name: "an answer without the sponsors", ids: []string{agentID}, bodies: []string{batchOf(t, ok("1", `{"id":"`+agentID+`","accountEnabled":true}`))},
			fail: []string{"without that agent identity and its list of sponsors"}},
		{name: "a sponsor without an id", ids: []string{agentID}, bodies: []string{batchOf(t, ok("1", agentOf(agentID, `{"@odata.type":"#microsoft.graph.user"}`)))},
			fail: []string{"a sponsor that has no id"}},
		{name: "a batch that is not JSON", ids: []string{agentID}, bodies: []string{"<html>"}, fail: []string{"a batch Straza cannot read"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps := make([]step, len(tt.bodies))
			for i, body := range tt.bodies {
				steps[i] = batchStep(body)
			}
			f := newFixture(t, steps...)
			got, err := f.client.Agents(context.Background(), tt.ids)
			f.done()
			if tt.fail == nil {
				if err != nil || !reflect.DeepEqual(got, tt.want) {
					t.Errorf("Agents = %+v, %s\nwant %+v", got, f.show(err), tt.want)
				}
				return
			}
			f.wantError(err, tt.fail...)
			if got != nil {
				t.Errorf("Agents = %+v, want no agents on a failed read", got)
			}
			var ge *Error
			if errors.As(err, &ge) != (tt.status != 0) || (tt.status != 0 && (ge.Status != tt.status || ge.RetryAfter != tt.retry)) {
				t.Errorf("err = %s, want an *Error with status %d and wait %s", f.show(err), tt.status, tt.retry)
			}
		})
	}
}

// batchOfAll answers a read of each of ids with that agent and no sponsor.
func batchOfAll(t *testing.T, ids []string) string {
	t.Helper()
	replies := make([]reply, len(ids))
	for i, id := range ids {
		replies[i] = reply{id: strconv.Itoa(i + 1), status: 200, body: agentOf(id)}
	}
	return batchOf(t, replies...)
}

func TestAgentsOuterFailure(t *testing.T) {
	f := newFixture(t, step{method: http.MethodPost, path: "/v1.0/$batch", status: 429, body: "@error-throttled.json", header: map[string]string{"Retry-After": "7"}})
	got, err := f.client.Agents(context.Background(), []string{agentID})
	f.done()
	f.wantError(err, "the read of agent identities with HTTP 429", "wait 7 seconds")
	var ge *Error
	if got != nil || !errors.As(err, &ge) || ge.Status != 429 {
		t.Errorf("Agents = %+v, %s, want no agents and an *Error with status 429", got, f.show(err))
	}
}

func TestAgentsRefusesBadID(t *testing.T) {
	tests := [][]string{
		{""},
		{"not-a-guid"},
		{agentID, "../users/" + userPat},
		{agentID + "?$select=id"},
		{agentID, agentID + "/sponsors"},
	}
	for _, ids := range tests {
		t.Run(fmt.Sprintf("%q", ids), func(t *testing.T) {
			f := newFixture(t)
			got, err := f.client.Agents(context.Background(), ids)
			f.wantError(err, "is not an Entra object ID", "Correct the Entra agent ID")
			if got != nil {
				t.Errorf("Agents = %+v, want none", got)
			}
			if n := f.tokenRequests(); n != 0 {
				t.Errorf("token requests = %d, want none before a refused ID", n)
			}
			f.done()
		})
	}
}
