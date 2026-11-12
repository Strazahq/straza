package clientcredentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/authn"
)

const (
	// entraExchangeScope is the scope of the exchange token a blueprint
	// asks for on behalf of one of its agent identities.
	entraExchangeScope = "api://AzureADTokenExchange/.default"
	// maxEntraCodes caps the numeric error codes the log keeps of one answer.
	maxEntraCodes = 5
)

// entraID matches the GUID form of the trace and correlation ids Entra
// answers with. Anything else is withheld from the log.
var entraID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// exchangeStep is one of the two grants of the Entra exchange.
type exchangeStep int

const (
	blueprintGrant exchangeStep = iota + 1
	agentGrant
)

func (s exchangeStep) String() string {
	if s == blueprintGrant {
		return "blueprint"
	}
	return "agent identity"
}

// exchange gets the token of the request's Entra agent identity in the two
// grants of the Entra agent identity exchange, inside the one fetch limit.
// The blueprint signs in with Straza's certificate assertion and asks for an
// exchange token for the agent identity, named in fmi_path. The agent
// identity then presents that token as its client assertion and gets the
// token of the configured resource. The exchange token lives only in this
// function: it is sent once and never kept, logged or returned.
func (t *Tokens) exchange(ctx context.Context, p Provider, r Request) (string, time.Duration, error) {
	assertion, kid, err := t.signer.SignCertificateAssertion(t.now(), p.Blueprint, p.TokenURL)
	if err != nil {
		return "", 0, t.unsignedEntra(p, r, err)
	}
	first := url.Values{
		"grant_type":            {"client_credentials"},
		"client_id":             {p.Blueprint},
		"client_assertion_type": {assertionType},
		"client_assertion":      {assertion},
		"scope":                 {entraExchangeScope},
		"fmi_path":              {r.EntraAgentID},
	}
	exchangeToken, _, err := t.grant(ctx, p, r, first, p.Blueprint, entraRefused(p, r, blueprintGrant, kid))
	if err != nil {
		return "", 0, err
	}
	second := url.Values{
		"grant_type":            {"client_credentials"},
		"client_id":             {r.EntraAgentID},
		"client_assertion_type": {assertionType},
		"client_assertion":      {exchangeToken},
		"scope":                 {strings.Join(p.Scopes, " ")},
	}
	token, lifetime, err := t.grant(ctx, p, r, second, r.EntraAgentID, entraRefused(p, r, agentGrant, kid))
	if err != nil {
		return "", 0, err
	}
	t.log.Debug("client credentials: Entra agent identity token fetched", "provider", r.Provider, "server", r.Server,
		"agent", r.ClientID, "entra_agent_id", r.EntraAgentID, "lifetime", lifetime.String())
	return token, lifetime, nil
}

// unsignedEntra is the refusal for a certificate assertion that could not be
// signed. A staged key needs its certificate on the blueprint before its
// promotion, so that sentence names the upload, and every other state reads
// as on any provider.
func (t *Tokens) unsignedEntra(p Provider, r Request, err error) *refusal {
	var staged *authn.StagedKeyError
	if errors.As(err, &staged) {
		return refuse(r, "The client assertion key %s is staged and signs nothing yet. An Agent ID Administrator uploads its certificate, which strazactl signing-keys certificate client_assertion %s prints, "+
			"to the Entra blueprint %s, then an administrator promotes it with strazactl signing-keys promote client_assertion %s, and the agent calls again.",
			staged.KID, staged.KID, p.Blueprint, staged.KID)
	}
	return t.unsigned(r, err)
}

// entraRefused handles Entra's refusal of one step. The log gets Entra's
// error, its numeric codes and the trace and correlation ids, which find the
// request in Entra's sign-in log, and never the description, which names
// tenants and applications and may repeat what was sent. The sentence names
// the step, because the fix differs: the blueprint is the identity team's or
// the config's, the agent identity is that agent's.
func entraRefused(p Provider, r Request, step exchangeStep, kid string) onRefused {
	return func(at string, status int, body []byte, warn warnFunc) *refusal {
		var e struct {
			Error         string          `json:"error"`
			Codes         json.RawMessage `json:"error_codes"`
			TraceID       string          `json:"trace_id"`
			CorrelationID string          `json:"correlation_id"`
		}
		if json.Unmarshal(body, &e) != nil || e.Error == "" {
			warn("the token endpoint answered without an OAuth error", "step", step.String())
			return noOAuthError(r, at, status)
		}
		code, codes := plainCode(e.Error), entraCodes(e.Codes)
		warn("Entra refused the "+step.String()+" step of the agent identity exchange",
			"step", step.String(), "error", code, "error_codes", codes,
			"trace_id", entraLogID(e.TraceID), "correlation_id", entraLogID(e.CorrelationID),
			"blueprint", p.Blueprint, "entra_agent_id", r.EntraAgentID)
		var aadsts int64
		if len(codes) > 0 {
			aadsts = codes[0]
		}
		return entraSentence(p, r, step, kid, code, aadsts)
	}
}

// entraSentence is the refusal for Entra's answer to one step, by the first
// of its numeric codes. The codes with a fix of their own were seen in the
// lab tenant, and every other code names the step and where to look.
func entraSentence(p Provider, r Request, step exchangeStep, kid, code string, aadsts int64) *refusal {
	switch {
	case step == blueprintGrant && aadsts == 700027:
		return asked(r, "Entra refused the blueprint %s (AADSTS700027), because it holds no certificate of the client assertion key %s. "+
			"An Agent ID Administrator uploads the certificate that strazactl signing-keys certificate client_assertion %s prints to that blueprint, then the agent calls again.",
			p.Blueprint, kid, kid)
	case step == blueprintGrant && aadsts == 7000112:
		return asked(r, "Entra says the blueprint %s is disabled (AADSTS7000112), so no agent of it gets a token. Ask the identity team why it was disabled, and call again once it is enabled.", p.Blueprint)
	case step == blueprintGrant && aadsts == 700016:
		return asked(r, "Entra has no blueprint %s in tenant %s (AADSTS700016). "+
			"Check oauth.providers.%s.clientCredentials.entraBlueprint in strazad's config against the agent blueprint in the Entra admin centre, then call again.",
			p.Blueprint, p.Tenant, r.Provider)
	case step == agentGrant && aadsts == 700016:
		return asked(r, "Entra has no agent identity %s in tenant %s (AADSTS700016). "+
			"Check entraAgentId on %s's Straza record against the agent identity in the Entra admin centre, correct it with strazactl users entra-agent-id set, then call again.",
			r.EntraAgentID, p.Tenant, r.ClientID)
	case step == agentGrant && aadsts == 7000112:
		return asked(r, "Entra says the agent identity %s is disabled (AADSTS7000112). Ask the identity team why it was disabled, and call again once it is enabled.", r.EntraAgentID)
	case step == agentGrant && aadsts == 7002134:
		return asked(r, "Entra refused the agent identity %s at the second step (AADSTS7002134), which Entra answers when the agent identity sits under another blueprint than %s, the one Straza signs in as. "+
			"Check entraAgentId on %s's Straza record against that blueprint's agent identities in the Entra admin centre, correct it with strazactl users entra-agent-id set, then call again.",
			r.EntraAgentID, p.Blueprint, r.ClientID)
	}
	who, which := "the blueprint "+p.Blueprint, "first"
	if step == agentGrant {
		who, which = "the agent identity "+r.EntraAgentID, "second"
	}
	with := code
	if with == "" {
		with = "an error code Straza does not repeat"
	}
	if aadsts > 0 {
		with += fmt.Sprintf(" (AADSTS%d)", aadsts)
	}
	return asked(r, "Entra refused %s at the %s step of the exchange with %s. "+
		"An administrator finds the request in Entra's sign-in log by the trace id in the strazad log, then the agent calls again.", who, which, with)
}

// entraCodes keeps the numeric error codes of an answer: whole positive
// numbers below a billion, at most maxEntraCodes of them. Anything else in
// the field is dropped.
func entraCodes(raw json.RawMessage) []int64 {
	var vals []any
	if json.Unmarshal(raw, &vals) != nil {
		return nil
	}
	var out []int64
	for _, v := range vals {
		f, ok := v.(float64)
		if !ok || f <= 0 || f >= 1e9 || f != math.Trunc(f) {
			continue
		}
		if out = append(out, int64(f)); len(out) == maxEntraCodes {
			break
		}
	}
	return out
}

// entraLogID is a trace or correlation id as the log may carry it: a GUID in
// lower case, empty when absent, and withheld when it is anything else.
func entraLogID(id string) string {
	switch {
	case id == "":
		return ""
	case entraID.MatchString(id):
		return strings.ToLower(id)
	}
	return "withheld"
}
