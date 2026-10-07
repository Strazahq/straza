package server

import (
	"context"
	"log/slog"
	"strings"

	"github.com/strazahq/straza/internal/clientcredentials"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/manager"
)

// agentTokens hands the client credentials lane to the manager, which names
// the request in its own type. Both types hold the same fields in the same
// order, so the conversion is one the compiler checks.
type agentTokens struct{ lane *clientcredentials.Tokens }

func (a agentTokens) Token(ctx context.Context, r manager.ClientTokenRequest) (string, error) {
	return a.lane.Token(ctx, clientcredentials.Request(r))
}

// buildClientTokens constructs the client credentials lane and returns the
// names of the providers in it. The lane takes every setting from the
// operator's config: a provider is in it only when its clientCredentials
// block says that the provider trusts this deployment's client assertion
// keys. Every user and session revocation drops that principal's tokens.
func (a *App) buildClientTokens(cfg config.Config, log *slog.Logger) []string {
	var withBlock []string
	providers := map[string]clientcredentials.Provider{}
	for name, p := range cfg.OAuth.Providers {
		if cc := p.ClientCredentials; cc != nil {
			withBlock = append(withBlock, name)
			providers[name] = clientcredentials.Provider{TokenURL: p.TokenURL, Audience: cc.AssertionAudience, Scopes: cc.Scopes}
		}
	}
	a.clientTokens = clientcredentials.New(clientcredentials.Options{
		Providers: providers, Signer: a.assertionKeys, Log: log,
		KeysURL: strings.TrimRight(cfg.Server.PublicURL, "/") + clientAssertionJWKSPath,
	})
	a.denylist.onUser, a.denylist.onSession = a.clientTokens.DropUser, a.clientTokens.DropSession
	return withBlock
}
