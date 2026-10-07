package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
)

// waivedRunner is the command server runner whose arguments hold a plain
// word after a flag that names a secret, with the given description.
func waivedRunner(name, description string) string {
	return "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: " + name + "\n  description: " + description + "\nserver:\n  name: io.x/" + name + "\n  version: 1.0.0\n" +
		"straza:\n  runtime:\n    kind: command\n    command:\n      exec: /bin/sh\n      args: [-c, \"exec sleep 30\", --auth, oauth]\n"
}

// TestDirectRoutesWaiveWhatLiveStateHolds pins the waiver on the direct
// routes: a live server whose stored manifest holds a plain word after a
// flag naming a secret takes a new description, and a live role whose name
// holds an invisible character takes one too, each recorded with the
// waiver's warning, while the same manifest for a new server and the same
// character in a new role's name are still refused with nothing stored.
func TestDirectRoutesWaiveWhatLiveStateHolds(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	putServer(t, f.app, waivedRunner("runner", "The runner."))
	odd, err := f.app.store.Roles().Create(context.Background(), store.Role{Name: "dev\u200bops", Kind: store.RoleKindBusiness})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		req    directReq
		status int
		object string
	}{
		{"a new description of the server", directReq{method: http.MethodPost, path: "/v1/admin/apps", ctype: "application/yaml", body: []byte(waivedRunner("runner", "Changed."))},
			http.StatusCreated, "App/runner"},
		{"a new description of the role", jsonReq(t, http.MethodPatch, "/v1/admin/roles/"+odd.ID, map[string]any{"description": "New words."}), http.StatusOK, "Role/dev\u200bops"},
	} {
		code, out := f.send(t, f.root, tc.req)
		if code != tc.status {
			t.Fatalf("%s = %d %s, want %d", tc.name, code, out, tc.status)
		}
		row, items := f.newestDraft(t)
		var counts map[string]int
		if err := json.Unmarshal([]byte(row.CheckCounts), &counts); err != nil {
			t.Fatalf("%s: the minted draft's check counts %q: %v", tc.name, row.CheckCounts, err)
		}
		if row.State != string(drafts.StatePublished) || len(items) != 1 || items[0].Kind+"/"+items[0].Name != tc.object || counts["warnings"] < 1 {
			t.Errorf("%s minted the draft %s %v with the counts %s, want a published draft of %s with the waiver's warning counted", tc.name, row.State, items, row.CheckCounts, tc.object)
		}
	}
	for _, tc := range []struct {
		name   string
		req    directReq
		status int
		words  string
	}{
		{"the same manifest for a new server", directReq{method: http.MethodPost, path: "/v1/admin/apps", ctype: "application/yaml", body: []byte(waivedRunner("runner2", "Another."))},
			http.StatusUnprocessableEntity, "A change cannot carry a secret"},
		{"the same character in a new role's name", jsonReq(t, http.MethodPost, "/v1/admin/roles", map[string]any{"name": "qa\u200bteam", "description": "New."}),
			http.StatusBadRequest, "holds an invisible character"},
	} {
		before := f.drafts(t)
		code, out := f.send(t, f.root, tc.req)
		var answer struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(out, &answer)
		if code != tc.status || !strings.Contains(answer.Error, tc.words) {
			t.Errorf("%s = %d %q, want %d with %q", tc.name, code, answer.Error, tc.status, tc.words)
		}
		if f.drafts(t) != before {
			t.Errorf("%s stored a draft", tc.name)
		}
	}
}

// TestWaivedIntakeSplitsTheFindings pins the split a door reads: the
// refusals live state does not waive, and the warnings it made of the
// rest, in intake's order.
func TestWaivedIntakeSplitsTheFindings(t *testing.T) {
	t.Parallel()
	w := drafts.World{Roles: map[string]drafts.Role{"dev\u200bops": {Name: "dev\u200bops"}}}
	d := drafts.Draft{Items: []drafts.Item{{Kind: drafts.KindRole, Name: "dev\u200bops", Op: drafts.OpRemove}, {Kind: drafts.KindRole, Name: "qa\u200bteam", Op: drafts.OpRemove}}}
	refused, warnings := waivedIntake(w, d, drafts.Intake(d, drafts.Principal{}))
	if len(refused) != 1 || len(warnings) != 1 || !strings.Contains(refused[0].Sentence, "document 2") || !strings.Contains(warnings[0].Sentence, "document 1") {
		t.Errorf("waivedIntake split into %v and %v, want the refusal of document 2 and the warning of document 1", refused, warnings)
	}
}

// TestDirectRoutesRefuseAnAgent pins that an agent with the admin grant
// that changes the description of a live role is refused on the direct
// route before intake, with the admin API's refusal of anyone who is not a
// person, so no draft is minted.
func TestDirectRoutesRefuseAnAgent(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ctx := context.Background()
	ro, err := f.app.store.Roles().Create(ctx, store.Role{Name: "équipe", Kind: store.RoleKindBusiness})
	if err != nil {
		t.Fatal(err)
	}
	u, err := f.app.store.Users().GetByUsername(ctx, "bot")
	if err != nil {
		t.Fatal(err)
	}
	grantAdmin(t, f.app, u.ID)
	bot := loginDeviceFlow(t, f.base, "bot", "hunter2!")
	code, out := f.send(t, bot, jsonReq(t, http.MethodPatch, "/v1/admin/roles/"+ro.ID, map[string]any{"description": "New words."}))
	var answer struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(out, &answer)
	if want := fmt.Sprintf(nonPersonAdminRefusal, "bot", "an agent"); code != http.StatusForbidden || answer.Error != want {
		t.Errorf("the agent's change of the live role = %d %q, want 403 %q", code, answer.Error, want)
	}
	if n := f.drafts(t); n != 0 {
		t.Errorf("the refused change minted %d drafts", n)
	}
}

// TestDirectRoutesWaiveOnlyForAReaderOfTheServer pins that the direct
// install route waives under the caller's read standing: an admin API
// token holding apps:write alone gets the same 422 with the same words
// for the stored value and for a wrong guess, so it learns nothing its
// read routes would not show, and a token that also holds apps:read is
// waived for the stored value and refused for the wrong one.
func TestDirectRoutesWaiveOnlyForAReaderOfTheServer(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	putServer(t, f.app, waivedRunner("runner", "The runner."))
	mint := func(name, scope string) string {
		var minted struct {
			Token string `json:"token"`
		}
		if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/api-tokens", f.root, map[string]any{"name": name, "scope": scope}, &minted); code != http.StatusCreated {
			t.Fatalf("mint %s = %d", name, code)
		}
		return minted.Token
	}
	writer, reader := mint("writer", "apps:write"), mint("reader", "apps:read,apps:write")
	right, wrong := waivedRunner("runner", "Changed."), strings.Replace(waivedRunner("runner", "Changed."), "oauth]", "oauth2]", 1)
	install := func(bearer, doc string) (int, string) {
		code, out := f.send(t, bearer, directReq{method: http.MethodPost, path: "/v1/admin/apps", ctype: "application/yaml", body: []byte(doc)})
		return code, string(out)
	}
	before := f.drafts(t)
	codeRight, outRight := install(writer, right)
	codeWrong, outWrong := install(writer, wrong)
	if codeRight != http.StatusUnprocessableEntity || codeWrong != codeRight || outRight != outWrong || !strings.Contains(outRight, "Only someone who can read that server can send that value") {
		t.Errorf("the write-only token: the stored value = %d %s, a wrong guess = %d %s; want the same 422 naming a reader of the server", codeRight, outRight, codeWrong, outWrong)
	}
	if f.drafts(t) != before {
		t.Errorf("the write-only token stored a draft")
	}
	if code, out := install(reader, right); code != http.StatusCreated {
		t.Errorf("the reading token's stored value = %d %s, want 201", code, out)
	}
	if code, out := install(reader, wrong); code != http.StatusUnprocessableEntity || !strings.Contains(out, "cannot carry a secret") {
		t.Errorf("the reading token's wrong guess = %d %s, want 422 with the secret refusal", code, out)
	}
}

// TestReaderWaivedKeepsMasks pins the waiver of a mask sent back, in the
// reader's view: for a reader of the server a mask at a place the live
// manifest still masks is waived into a warning that says the publish
// keeps the stored value, two such masks and a plain env value the answer
// masked included. A mask at a changed place stays refused, the sentence
// naming that place even when an earlier mask fits, and so does a
// document with an anchor, whose sentence says why. A server whose name
// the scan withholds is neither waived nor reworded, so no new sentence
// quotes the name. A caller who may not read the server is refused with
// the reader's words after the read refusal, the same for a mask that fits
// and for one that does not, so the answer confirms nothing about the
// stored manifest.
func TestReaderWaivedKeepsMasks(t *testing.T) {
	t.Parallel()
	live := manifestJSON(t, maskedRunner("The runner."))
	const slack = "xoxb-2745010221-2745010221000-abcdefghij0123456789"
	withheld := manifestJSON(t, draftApp(slack, "https://slack.example/mcp", "The Slack server."))
	w := drafts.World{Apps: map[string]drafts.App{"runner": {Name: "runner", Manifest: live, AdminRole: "mcp-admin-runner"},
		slack: {Name: slack, Manifest: withheld, AdminRole: "mcp-admin-" + slack}}, Roles: map[string]drafts.Role{}}
	answer := maskedManifest(live)
	changed := strings.Replace(maskedRunner("Changed."), "value: hunter2hunter2", "value: '[REDACTED]'", 1) + "        - {name: EXTRA, value: '[REDACTED]'}\n"
	aliased := strings.Replace(maskedRunner("Changed."), "value: hunter2hunter2", "value: &tok '[REDACTED]'", 1) + "      workdir: *tok\n"
	reader, blind := personCaller("kim", "apps:read", "drafts:write"), personCaller("ada", "drafts:read", "drafts:write")
	const readerFix = "Only someone who can read that server can send back a value Straza masked, because only a reader of the server may learn whether its stored manifest holds a value there. " +
		"Drafting the server runner needs the scope apps:read or the role mcp-admin-runner of the server runner, because a draft shows the live config of the servers it names and what each gives a role. " +
		"Ask an administrator for that grant, or leave the server runner out of the draft."
	const keptFix = "Nothing needs to change. To change that value, write the new value in its place."
	cases := []struct {
		name    string
		caller  draftCaller
		doc     string
		waived  string
		refused string
		fix     string
	}{
		{"a reader sends the answer back", reader, strings.Replace(answer, "The runner.", "Changed.", 1),
			"App/runner holds a value that Straza masked for display at straza.runtime.command.args[3], and the server's stored manifest already holds a value there, so the publish keeps the stored value.", "", keptFix},
		{"a reader sends a plain env value the answer masked", reader, strings.Replace(maskedRunner("Changed."), "value: debug", "value: '[REDACTED]'", 1),
			"App/runner holds a value that Straza masked for display at straza.runtime.command.env[0].value, and the server's stored manifest already holds a value there, so the publish keeps the stored value.", "", keptFix},
		{"a reader sends two masks", reader, strings.Replace(strings.Replace(maskedRunner("Changed."), "value: debug", "value: '[REDACTED]'", 1), "value: hunter2hunter2", "value: '[REDACTED]'", 1),
			"App/runner holds a value that Straza masked for display at straza.runtime.command.env[0].value, and the server's stored manifest already holds a value there, so the publish keeps the stored value.", "", keptFix},
		{"a reader sends a mask at a changed place after one that fits", reader, changed, "",
			"App/runner holds a value that Straza masked for display at straza.runtime.command.env[2].value, and the stored manifest as Straza answers it holds no mask there, so publishing it would store the mask in place of the value.",
			"Write the real value in place of the mask, or leave that place exactly as strazactl apps export prints it to keep the stored value, and send the draft again."},
		{"a reader sends an anchored mask", reader, aliased, "",
			"App/runner holds a value that Straza masked for display at straza.runtime.command.env[1].value, and an anchor, an alias or a merge key in the document could copy the stored value to another place, so the mask is not waived.",
			"Write the document without anchors, aliases and merge keys, and send the draft again."},
		{"a reader sends back a masked value of a server whose name is withheld", reader, strings.Replace(draftApp(slack, "https://slack.example/mcp", "Changed."), "value: team-a", "value: '[REDACTED]'", 1), "",
			"App/(name withheld) holds a value that Straza masked for display at server.remotes[0].headers[0].value, so publishing it would store the mask in place of the value.",
			"Write the real value in place of the mask, or leave that place exactly as strazactl apps export prints it to keep the stored value, and send the draft again."},
		{"a mask on a server that is not live", reader, strings.Replace(answer, "name: runner", "name: runner2", 2), "",
			"App/runner2 holds a value that Straza masked for display at straza.runtime.command.args[3], so publishing it would store the mask in place of the value.",
			"Write the real value in place of the mask, or leave that place exactly as strazactl apps export prints it to keep the stored value, and send the draft again."},
		{"a caller who may not read the server sends the answer back", blind, strings.Replace(answer, "The runner.", "Changed.", 1), "",
			"App/runner holds a value that Straza masked for display at straza.runtime.command.args[3], so publishing it would store the mask in place of the value.", readerFix},
		{"a caller who may not read the server sends a mask at a changed place", blind, strings.Replace(changed, "value: hunter2hunter2", "value: '[REDACTED]'", 1), "",
			"App/runner holds a value that Straza masked for display at straza.runtime.command.env[1].value, so publishing it would store the mask in place of the value.", readerFix},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items, bundle := drafts.ParseBundle([]string{tc.doc})
			if len(bundle) > 0 {
				t.Fatalf("the document does not read: %+v", bundle)
			}
			d := drafts.Draft{Items: items, Authors: []drafts.Principal{tc.caller.author}}
			fs := drafts.Intake(d, tc.caller.author)
			refused, warnings := tc.caller.readerWaived(w, d, fs)
			masked := func(fs []drafts.Finding) []drafts.Finding {
				return slices.DeleteFunc(slices.Clone(fs), func(f drafts.Finding) bool { return f.Code != "bundle.masked" })
			}
			got, other, class := masked(warnings), masked(refused), drafts.ClassWarning
			if tc.refused != "" {
				got, other, class = other, got, drafts.ClassRefused
			}
			if len(other) != 0 {
				t.Errorf("bundle.masked is also %s: %+v", other[0].Class, other)
			}
			if len(got) != 1 || got[0].Class != class || got[0].Sentence != tc.waived+tc.refused || got[0].Fix != tc.fix {
				t.Errorf("got %+v\nwant one %s bundle.masked with the sentence %q\nand the fix %q", got, class, tc.waived+tc.refused, tc.fix)
			}
		})
	}
}
