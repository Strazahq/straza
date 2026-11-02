package main

import "testing"

// TestServiceReplacesDaemonAndDrain pins the command surface: `straza
// service` is registered and takes no operand, and `straza daemon` and
// `straza drain` are gone, not aliased.
func TestServiceReplacesDaemonAndDrain(t *testing.T) {
	root := rootCmd()
	c, _, err := root.Find([]string{"service"})
	if err != nil || c.Name() != "service" {
		t.Fatalf("service is not registered on the root: %v", err)
	}
	if err := c.Args(c, []string{"run"}); err == nil {
		t.Error("service accepted an operand")
	}
	for _, gone := range []string{"daemon", "drain"} {
		for _, sub := range root.Commands() {
			if sub.Name() == gone || sub.HasAlias(gone) {
				t.Errorf("%s is still a command or an alias (%s)", gone, sub.Name())
			}
		}
	}
}
