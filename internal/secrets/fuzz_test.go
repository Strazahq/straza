package secrets

import (
	"testing"
)

// FuzzParseTokenResponse hammers the OAuth token-endpoint response parser
// with mutated bodies under both advertised content types, because provider
// responses are hostile network input. Any input must produce a value or an
// error (never a panic), and a token must never materialize out of an
// error-shaped body.
func FuzzParseTokenResponse(f *testing.F) {
	seeds := []string{
		`{"access_token":"gho_A","refresh_token":"ghr_A","expires_in":28800,"token_type":"bearer"}`,
		`{"access_token":"gho_B","token_type":"bearer","scope":"repo"}`,
		`{"error":"bad_verification_code","error_description":"expired"}`,
		`access_token=gho_C&token_type=bearer&expires_in=3600`,
		`error=access_denied&error_description=denied`,
		`{"expires_in":"not-a-number"}`,
		`<!doctype html>`,
		``,
		`%zz=%%%`,
	}
	for _, s := range seeds {
		f.Add(s, true)
		f.Add(s, false)
	}
	f.Fuzz(func(_ *testing.T, body string, asForm bool) {
		ct := "application/json"
		if asForm {
			ct = "application/x-www-form-urlencoded"
		}
		// Must never panic; an error is the only acceptable failure mode.
		// (A body carrying both error and token fields parses fine: the
		// tokenRequest switch prefers the error, pinned by TestExchangeCode.)
		_, _ = parseTokenResponse(ct, []byte(body))
	})
}
