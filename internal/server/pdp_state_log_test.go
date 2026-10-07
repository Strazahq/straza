package server

import (
	"strings"
	"testing"
)

// TestDenylistLogsAtDebug pins the denylist transition line: every denylist
// mutation writes exactly one Debug "denylist updated" record carrying
// component, op, scope and id, never Info; a nil logger is silent and the
// set behaves the same.
func TestDenylistLogsAtDebug(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, op, scope, id string
		do                  func(d *denylist)
	}{
		{"revoke session", "revoke", "session", "s-1", func(d *denylist) { d.RevokeSession("s-1") }},
		{"revoke user", "revoke", "user", "u-1", func(d *denylist) { d.RevokeUser("u-1") }},
		{"revoke device", "revoke", "device", "d-1", func(d *denylist) { d.RevokeDevice("d-1") }},
		{"allow user", "allow", "user", "u-1", func(d *denylist) { d.AllowUser("u-1") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log, buf := captureLogger()
			d := newDenylist(log)
			tc.do(d)
			lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
			if len(lines) != 1 || lines[0] == "" {
				t.Fatalf("records = %d, want exactly 1:\n%s", len(lines), buf.String())
			}
			for _, want := range []string{"level=DEBUG", `msg="denylist updated"`, "component=denylist", "op=" + tc.op, "scope=" + tc.scope, "id=" + tc.id} {
				if !strings.Contains(lines[0], want) {
					t.Fatalf("record lacks %s: %s", want, lines[0])
				}
			}
		})
	}

	quiet := newDenylist(nil)
	quiet.RevokeUser("u-2")
	if !quiet.userBlocked("u-2") {
		t.Fatal("nil logger must not change the set's behaviour")
	}
	quiet.AllowUser("u-2")
	if quiet.userBlocked("u-2") {
		t.Fatal("allow did not lift the entry")
	}
}
