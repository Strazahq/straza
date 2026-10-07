package e2ematrix

import (
	"context"
	"fmt"
)

// adminBearer returns the token an admin step acts with: the login token of
// the person the step names under as, or the operator's login token when it
// names nobody.
func (s *scenarioRun) adminBearer(ctx context.Context, st *step) (string, error) {
	as := st.str("as")
	if as == "" {
		return s.stack.server.admin, nil
	}
	return s.personToken(ctx, as)
}

// personToken signs a declared human in through the built-in issuer's device
// flow and keeps the token for the rest of the run, so several steps may act
// as the same person. The decide routes take a person's own token and refuse
// a token that carries no user, which is why a step that decides names an
// identity instead of borrowing the operator's.
func (s *scenarioRun) personToken(ctx context.Context, name string) (string, error) {
	if token, ok := s.tokens[name]; ok {
		return token, nil
	}
	token, err := s.stack.server.deviceToken(ctx, name, s.sc.Identities[name].Password)
	if err != nil {
		return "", fmt.Errorf("sign in as %s: %w (does a step create the user with this password first?)", name, err)
	}
	s.tokens[name] = token
	return token, nil
}
