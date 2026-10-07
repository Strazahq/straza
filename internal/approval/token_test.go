package approval

import (
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

func testTokenService(t *testing.T, now time.Time) *Service {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return &Service{tokenKey: key, now: func() time.Time { return now }}
}

// tamperSig flips the FIRST character of the signature part to a guaranteed-
// different value. Tampering the LAST char is not a reliable tamper: the MAC is
// unpadded base64url (32 bytes → 43 chars), so the final char carries only 4
// meaningful bits and Go's non-Strict decoder ignores the 2 trailing slack
// bits: several distinct final chars (and the identical one, 1/64 per run
// under a random key) decode to byte-identical MACs, making verification
// rightly succeed, so a last-char tamper would make this test flake.
func tamperSig(tok string) string {
	dot := strings.IndexByte(tok, '.')
	first := dot + 1
	replacement := byte('A')
	if tok[first] == 'A' {
		replacement = 'B'
	}
	return tok[:first] + string(replacement) + tok[first+1:]
}

func TestDecisionTokenRoundTrip(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := testTokenService(t, now)
	exp := now.Add(time.Hour)

	tok, err := s.MintDecisionToken("appr-1", "approved", exp)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if err := s.VerifyDecisionToken(tok, "appr-1", "approved"); err != nil {
		t.Errorf("verify good token: %v", err)
	}

	tests := []struct {
		name    string
		tok     string
		id      string
		verdict string
		want    error
	}{
		{"wrong verdict", tok, "appr-1", "denied", ErrBadToken},
		{"wrong id", tok, "appr-2", "approved", ErrBadToken},
		{"garbage", "not-a-token", "appr-1", "approved", ErrBadToken},
		{"no dot", "abcdef", "appr-1", "approved", ErrBadToken},
		{"tampered sig", tamperSig(tok), "appr-1", "approved", ErrBadToken},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.VerifyDecisionToken(tc.tok, tc.id, tc.verdict); err != tc.want {
				t.Errorf("verify = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestDecisionTokenExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := testTokenService(t, now)
	tok, _ := s.MintDecisionToken("appr-1", "approved", now.Add(time.Minute))

	// Advance the clock past expiry.
	s.now = func() time.Time { return now.Add(2 * time.Minute) }
	if err := s.VerifyDecisionToken(tok, "appr-1", "approved"); err != ErrTokenExpired {
		t.Errorf("expired token = %v, want ErrTokenExpired", err)
	}
}

func TestDecisionTokenKeyIsolation(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a := testTokenService(t, now)
	b := testTokenService(t, now)
	tok, _ := a.MintDecisionToken("appr-1", "approved", now.Add(time.Hour))
	if err := b.VerifyDecisionToken(tok, "appr-1", "approved"); err == nil {
		t.Error("a token minted under one key must not verify under another")
	}
}
