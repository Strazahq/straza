package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestDirectInstallWordsTheChange pins the words of the read-standing
// refusal on a direct install: an admin API token holding apps:write alone
// reads that changing the server needs apps:read and to make the change
// again, never that the server should be left out of a draft, because the
// token sent no draft. The stored value and a wrong guess read the same,
// so the token learns nothing its read routes would not show.
func TestDirectInstallWordsTheChange(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	putServer(t, f.app, waivedRunner("runner", "The runner."))
	var minted struct {
		Token string `json:"token"`
	}
	if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/api-tokens", f.root, map[string]any{"name": "writer", "scope": "apps:write"}, &minted); code != http.StatusCreated {
		t.Fatalf("mint writer = %d", code)
	}
	right := waivedRunner("runner", "Changed.")
	wrong := strings.Replace(right, "oauth]", "oauth2]", 1)
	install := func(doc string) (int, string) {
		code, out := f.send(t, minted.Token, directReq{method: http.MethodPost, path: "/v1/admin/apps", ctype: "application/yaml", body: []byte(doc)})
		return code, string(out)
	}
	const want = "Changing the server runner needs the scope apps:read, because a change is checked against the live config of the servers it names and what each gives a role. " +
		"Mint a token that also holds that scope with strazactl api-token create, then make the change again."
	codeRight, outRight := install(right)
	codeWrong, outWrong := install(wrong)
	switch {
	case codeRight != http.StatusUnprocessableEntity || codeWrong != codeRight || outRight != outWrong:
		t.Errorf("the stored value = %d %s, a wrong guess = %d %s; want the same 422 for both", codeRight, outRight, codeWrong, outWrong)
	case !strings.Contains(outRight, want) || strings.Contains(outRight, "out of the draft"):
		t.Errorf("the refusal reads %s, want it to hold %q and no draft words", outRight, want)
	}
}
