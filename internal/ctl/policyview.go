package ctl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// PolicySummary mirrors the server-computed decomposition of a stored set
// (internal/server policySummary; pkg/api/openapi.yaml is the contract).
// Nil on PolicyDetail means the stored source no longer parses: absence is
// the honest signal, never an invented shape.
type PolicySummary struct {
	Name        string                    `json:"name"`
	Description string                    `json:"description,omitempty"`
	Priority    int                       `json:"priority"`
	Rules       int                       `json:"rules"`
	Postures    map[string]int            `json:"postures,omitempty"`
	MatchRoles  []string                  `json:"matchRoles,omitempty"`
	MatchOther  bool                      `json:"matchOther,omitempty"`
	Capture     string                    `json:"capture,omitempty"`
	Lanes       map[string]map[string]int `json:"lanes,omitempty"`
}

// PolicyDetail is the single-set GET representation (0.78.0): the full
// stored YAML plus the server's own summary/drift facts. Kept separate from
// the summary-only list PolicySet so each mirrors exactly what its endpoint
// returns.
type PolicyDetail struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Priority int    `json:"priority"`
	Status   string `json:"status"`
	YAML     string `json:"yaml"`
	// UpdatedAt is RFC3339 (UTC) as the server emits it.
	UpdatedAt string `json:"updated_at"`
	// Drift is a pointer on purpose: the server omits the field both for "no
	// drift" and for a legacy row whose compiled hash was never stamped, and
	// neither state warrants a claim. nil = no claim; only an explicit true
	// may be rendered as drift.
	Drift   *bool          `json:"drift"`
	Summary *PolicySummary `json:"summary"`
}

// errPolicyByNameSkew names the one server generation that can 404/405 the
// by-name read while still holding the set: the wording matches the
// policy-list envelope skew error so operators meet one sentence, not two.
const errPolicyByNameSkew = "this server predates the 0.78.0 policy read by name; upgrade strazad or use a matching strazactl"

// PolicyByName fetches one stored PolicySet in full. A 404 is ambiguous
// (missing set, or a pre-0.78.0 server that never had the route), so before
// claiming a set does not exist the list is probed: a name that IS listed
// turns the 404 into the version-skew error instead of a lie.
func (c *Client) PolicyByName(ctx context.Context, name string) (PolicyDetail, error) {
	var out PolicyDetail
	buf, code, err := c.doWithCode(ctx, http.MethodGet, "/v1/admin/policies/"+url.PathEscape(name))
	if err != nil {
		return out, err
	}
	switch code {
	case http.StatusOK:
		return out, json.Unmarshal(buf, &out)
	case http.StatusMethodNotAllowed:
		// Pre-0.78.0 the path existed for DELETE only.
		return out, fmt.Errorf("%s", errPolicyByNameSkew)
	case http.StatusNotFound:
		sets, lerr := c.Policies(ctx)
		if lerr != nil {
			if strings.Contains(lerr.Error(), "0.78.0") {
				return out, lerr // the envelope skew already names the fix
			}
			return out, fmt.Errorf("no policy set named %q on this server (the list could not be checked: %v)", name, lerr)
		}
		for _, s := range sets {
			if s.Name == name {
				return out, fmt.Errorf("%s", errPolicyByNameSkew)
			}
		}
		return out, fmt.Errorf("no policy set named %q on this server (the list was checked, so this is not version skew)", name)
	default:
		var apiErr struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(buf, &apiErr)
		if apiErr.Error == "" {
			apiErr.Error = fmt.Sprintf("HTTP %d", code)
		}
		return out, fmt.Errorf("%s", apiErr.Error)
	}
}

// doWithCode is DoBytes with the status code kept: the by-name read must
// tell 404 from 405 from an error body, which a flattened error string
// cannot.
func (c *Client) doWithCode(ctx context.Context, method, path string) ([]byte, int, error) {
	return c.sendCode(ctx, method, path, func(bearer string) ([]byte, int, error) {
		return c.doRaw(ctx, method, path, bearer, nil)
	})
}
