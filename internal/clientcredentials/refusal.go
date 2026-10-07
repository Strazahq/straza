package clientcredentials

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// refusal is a denied token request as the sentence the agent and the audit
// record read. It is built from Straza's own words, the configured names and
// at most the provider's error code, never from the provider's free text.
type refusal struct {
	sentence string
	// asked says the provider was asked, so the key pauses before it asks
	// again. A refusal that never left Straza pauses nothing.
	asked bool
}

func (r *refusal) Error() string { return r.sentence }

func asRefusal(err error, target **refusal) bool { return errors.As(err, target) }

// refuse opens every sentence the same way: who could not get which token.
func refuse(r Request, format string, args ...any) *refusal {
	return &refusal{sentence: fmt.Sprintf("agent %s could not get a %s token. ", r.ClientID, r.Server) + fmt.Sprintf(format, args...)}
}

// asked is refuse for an answer, or the lack of one, from the provider.
func asked(r Request, format string, args ...any) *refusal {
	out := refuse(r, format, args...)
	out.asked = true
	return out
}

// noSettings is the refusal for a provider without a clientCredentials block.
// An install refuses the same manifest with the same advice, so this one is
// met only when the block left the config after the install.
func noSettings(r Request) *refusal {
	return refuse(r, "Server %s sets credential.agents to client_credentials, and the provider %s has no clientCredentials settings. "+
		"Add oauth.providers.%s.clientCredentials.assertionAudience to strazad's config, or set credential.agents to own, sponsor or shared.",
		r.Server, r.Provider, r.Provider)
}

// paused repeats the refusal that began a pause and says when it ends.
func paused(last error, provider string, left time.Duration) *refusal {
	return &refusal{sentence: fmt.Sprintf("%s Straza asks %s again in %d seconds.", last, provider, int(math.Ceil(left.Seconds())))}
}
