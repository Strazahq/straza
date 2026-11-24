package entragraph

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

const (
	// maxBatch is the most requests Graph takes in one $batch.
	maxBatch = 20
	// agentRead follows an agent identity's ID in the address of its read,
	// which answers its names, its enabled state and each sponsor's type and
	// ID.
	agentRead = "/microsoft.graph.agentIdentity?$select=id,displayName,accountEnabled&$expand=sponsors($select=id)"
)

// sponsorKinds maps the types of an agent identity's sponsors.
var sponsorKinds = map[string]Kind{
	"#microsoft.graph.user":             KindUser,
	"#microsoft.graph.agentUser":        KindAgentUser,
	"#microsoft.graph.group":            KindGroup,
	"#microsoft.graph.servicePrincipal": KindServicePrincipal,
	"#microsoft.graph.agentIdentity":    KindAgentIdentity,
}

// Sponsor is one sponsor of an agent identity: its object ID and its kind,
// from its @odata.type, with KindOther for a type not listed.
type Sponsor struct {
	ID   string
	Kind Kind
}

// Agent is one agent identity as Agents reads it. Missing says Graph has no
// agent identity with the ID, and then only ID is set. Enabled is
// accountEnabled, nil when Graph left it out. Sponsors lists the sponsors in
// Graph's order, and is empty but not nil when Graph names none.
type Agent struct {
	ID          string
	DisplayName string
	Enabled     *bool
	Missing     bool
	Sponsors    []Sponsor
}

// batchRequest is one read inside a $batch.
type batchRequest struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	URL    string `json:"url"`
}

// Agents reads the agent identities ids names, each with its sponsors, in
// $batch requests of at most maxBatch reads, and answers one Agent per
// distinct ID, keyed by the ID as given. A read Graph answers 404 gives an
// Agent that is Missing. Any other failure of any read, and a batch that
// leaves a read unanswered, returns an error and no agents, because a caller
// disables an agent or drops its sponsor on what the map says. A read
// answered 400 or more is an *Error with that status. An ID that is not an
// Entra object ID is refused before any request, and no ID sends nothing.
func (c *Client) Agents(ctx context.Context, ids []string) (map[string]Agent, error) {
	var todo []string
	seen := map[string]bool{}
	for _, id := range ids {
		if !guidForm.MatchString(id) {
			return nil, errorf("Straza sent no read of agent identities, because %q is not an Entra object ID, which is a GUID. Correct the Entra agent ID that Straza holds for that agent.", id)
		}
		if !seen[id] {
			seen[id] = true
			todo = append(todo, id)
		}
	}
	out := make(map[string]Agent, len(todo))
	for len(todo) > 0 {
		n := min(len(todo), maxBatch)
		if err := c.agentBatch(ctx, todo[:n], out); err != nil {
			return nil, err
		}
		todo = todo[n:]
	}
	return out, nil
}

// agentBatch reads the agent identities ids, at most maxBatch of them, in
// one $batch request, and adds each to out. Graph answers the reads in any
// order, so each answer is matched to its read by the request id.
func (c *Client) agentBatch(ctx context.Context, ids []string, out map[string]Agent) error {
	what := "the read of agent identities"
	reqs := make([]batchRequest, len(ids))
	pending := make(map[string]string, len(ids))
	for i, id := range ids {
		reqs[i] = batchRequest{ID: strconv.Itoa(i + 1), Method: http.MethodGet, URL: "/servicePrincipals/" + id + agentRead}
		pending[reqs[i].ID] = id
	}
	body := struct {
		Requests []batchRequest `json:"requests"`
	}{reqs}
	ans, err := c.call(ctx, what, http.MethodPost, c.base+"/v1.0/$batch", body, "")
	if err != nil {
		return err
	}
	if ans.status != http.StatusOK {
		return unexpected(what, ans.status, "200")
	}
	var batch struct {
		Responses []struct {
			ID      string            `json:"id"`
			Status  int               `json:"status"`
			Headers map[string]string `json:"headers"`
			Body    json.RawMessage   `json:"body"`
		} `json:"responses"`
	}
	if json.Unmarshal(ans.body, &batch) != nil {
		return errorf("Microsoft Graph answered %s with a batch Straza cannot read, so Straza counts the read as incomplete.", what)
	}
	for _, r := range batch.Responses {
		id, ok := pending[r.ID]
		if !ok {
			return errorf("Microsoft Graph answered %s with a response to no read Straza sent, or to one it answered before, so Straza counts the read as incomplete.", what)
		}
		delete(pending, r.ID)
		agent, err := agentAnswer(id, r.Status, r.Headers, r.Body, ans.token)
		if err != nil {
			return err
		}
		out[id] = agent
	}
	for _, r := range reqs {
		if id, ok := pending[r.ID]; ok {
			return errorf("Microsoft Graph answered %s without an answer for agent identity %s, so Straza counts the read as incomplete.", what, id)
		}
	}
	return nil
}

// agentAnswer reads the answer to the read of the agent identity id inside a
// batch: the agent for a 200, a missing agent for a 404, and an error for
// any other status. token is the token the batch carried.
func agentAnswer(id string, status int, headers map[string]string, body json.RawMessage, token string) (Agent, error) {
	what := "the read of agent identity " + id
	switch {
	case status == http.StatusNotFound:
		return Agent{ID: id, Missing: true}, nil
	case status == http.StatusForbidden:
		return Agent{}, errorf("%w Grant the service app the application permission AgentIdentity.Read.All and give admin consent.", innerError(what, status, headers, body, token))
	case status >= 400:
		return Agent{}, innerError(what, status, headers, body, token)
	case status != http.StatusOK:
		return Agent{}, unexpected(what, status, "200 or 404")
	}
	var a struct {
		ID             string `json:"id"`
		DisplayName    string `json:"displayName"`
		AccountEnabled *bool  `json:"accountEnabled"`
		// Sponsors is nil when Graph left the list out, which is not the
		// same as a list with no sponsor.
		Sponsors *[]struct {
			Type string `json:"@odata.type"`
			ID   string `json:"id"`
		} `json:"sponsors"`
	}
	if json.Unmarshal(body, &a) != nil || !strings.EqualFold(a.ID, id) || a.Sponsors == nil {
		return Agent{}, errorf("Microsoft Graph answered %s without that agent identity and its list of sponsors, so Straza counts the read as incomplete.", what)
	}
	agent := Agent{ID: id, DisplayName: a.DisplayName, Enabled: a.AccountEnabled, Sponsors: make([]Sponsor, 0, len(*a.Sponsors))}
	for _, s := range *a.Sponsors {
		if s.ID == "" {
			return Agent{}, errorf("Microsoft Graph answered %s with a sponsor that has no id, so Straza counts the read as incomplete.", what)
		}
		kind, ok := sponsorKinds[s.Type]
		if !ok {
			kind = KindOther
		}
		agent.Sponsors = append(agent.Sponsors, Sponsor{ID: s.ID, Kind: kind})
	}
	return agent, nil
}

// innerError builds the *Error of a read inside a batch that Graph answered
// with a status of 400 or more, from the status, headers and body Graph gave
// that read, the same way an answer of its own would be built.
func innerError(what string, status int, headers map[string]string, body json.RawMessage, token string) *Error {
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}
	return graphError(what, &http.Response{StatusCode: status, Header: h}, body, token)
}
