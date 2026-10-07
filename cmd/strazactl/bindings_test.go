package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
)

// TestBindingsGroupHoldsListOnly pins that the bindings group offers list
// and no other verb in its help and its generated pages.
func TestBindingsGroupHoldsListOnly(t *testing.T) {
	var verbs []string
	for _, c := range bindingsCmd(nil).Commands() {
		if c.IsAvailableCommand() {
			verbs = append(verbs, c.Name())
		}
	}
	if !reflect.DeepEqual(verbs, []string{"list"}) {
		t.Errorf("bindings verbs = %v, want [list]", verbs)
	}
}

// TestBindingsApplyAnswersWithTheDraftsPath pins what the removed verb
// answers to the invocations older skills, pages and scripts still run: one
// sentence that names the drafts path, exit status 2 and no request, with a
// login or without one.
func TestBindingsApplyAnswersWithTheDraftsPath(t *testing.T) {
	const want = "strazactl bindings apply is gone: access rows now change through a draft that a person publishes. " +
		"Put the Role documents in a file, run strazactl drafts check -f <file>, then strazactl drafts create -f <file>"
	for _, tc := range []struct {
		name  string
		args  []string
		login bool
	}{
		{"apply with a file", []string{"bindings", "apply", "-f", "bindings.yaml"}, true},
		{"apply with a file, prune and yes", []string{"bindings", "apply", "-f", "bindings.yaml", "--prune", "--yes"}, true},
		{"apply alone", []string{"bindings", "apply"}, true},
		{"apply with a file and no login", []string{"bindings", "apply", "-f", "bindings.yaml"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer srv.Close()
			t.Setenv("STRAZA_SERVER", "")
			creds := noCreds(t)
			if tc.login {
				creds = writeCreds(t, srv.URL)
			}
			_, _, err := runCLI(t, creds, tc.args...)
			var ec exitCodeErr
			if !errors.As(err, &ec) || ec.code != 2 || err.Error() != want {
				t.Errorf("strazactl %v = %v, want exit status 2 with %q", tc.args, err, want)
			}
			if n := calls.Load(); n != 0 {
				t.Errorf("strazactl %v reached the server %d times", tc.args, n)
			}
		})
	}
}
