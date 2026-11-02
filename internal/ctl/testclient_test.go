package ctl

import "testing"

// testToken is the admin API token tokenClient sends.
const testToken = "wat_test"

// tokenClient is a client for base that sends testToken on every
// authenticated call.
func tokenClient(t *testing.T, base string) *Client {
	t.Helper()
	c := NewClient(base)
	c.APIToken = testToken
	return c
}
