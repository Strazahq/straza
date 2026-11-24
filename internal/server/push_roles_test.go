package server

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/membership"
	"github.com/strazahq/straza/internal/store"
)

// TestPushHubNamesEventBySubject pins how the hub names what it sends: a
// message goes only to the streams registered for its exact subject, under
// the event name that stream registered for it, and a subject no stream
// registered reaches nobody. So no message is ever sent as a revocation
// because nothing else claimed it.
func TestPushHubNamesEventBySubject(t *testing.T) {
	t.Parallel()
	h := newPushHub()
	register := func(session, user string) (int64, <-chan pushMsg) {
		id, ch, err := h.register(map[string]string{
			"straza.push.session." + session: pushEventRevocation,
			"straza.push.user." + user:       pushEventRevocation,
			rolesPushSubject(user):           pushEventRoles,
			policyPushSubject:                pushEventPolicy,
		}, 0)
		if err != nil {
			t.Fatal(err)
		}
		return id, ch
	}
	aliceID, alice := register("s1", "alice")
	_, bob := register("s2", "bob")
	next := func(ch <-chan pushMsg) string {
		select {
		case m := <-ch:
			return m.event
		default:
			return ""
		}
	}
	for _, tc := range []struct {
		name, subject      string
		wantAlice, wantBob string
	}{
		{"a role change of alice", "straza.push.roles.alice", pushEventRoles, ""},
		{"a role change of bob", "straza.push.roles.bob", "", pushEventRoles},
		{"a revocation of alice's user", "straza.push.user.alice", pushEventRevocation, ""},
		{"a revocation of bob's session", "straza.push.session.s2", "", pushEventRevocation},
		{"the policy broadcast", policyPushSubject, pushEventPolicy, pushEventPolicy},
		{"a subject class this server does not know", "straza.push.future.alice", "", ""},
		{"a role change of nobody connected", "straza.push.roles.carol", "", ""},
	} {
		h.fanout(tc.subject, []byte(`{}`))
		if a, b := next(alice), next(bob); a != tc.wantAlice || b != tc.wantBob {
			t.Errorf("%s: alice heard %q and bob %q, want %q and %q", tc.name, a, b, tc.wantAlice, tc.wantBob)
		}
	}

	h.unregister(aliceID)
	h.fanout("straza.push.roles.alice", []byte(`{}`))
	if a := next(alice); a != "" {
		t.Errorf("a stream that left still heard %q", a)
	}
	h.mu.Lock()
	_, kept := h.bySubject["straza.push.roles.alice"]
	h.mu.Unlock()
	if kept {
		t.Error("the hub keeps the subject of a stream that left")
	}
}

// TestPushRolesNudgeReachesOwnStream pins the role nudge on the stream of
// GET /v1/push: a session hears the nudge of its own user as the event
// roles, never the nudge of another user, and the session is not revoked by
// it.
func TestPushRolesNudgeReachesOwnStream(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	token, _ := checkinToken(t, app, base)

	lines, cancel, resp := sseStream(t, base, token)
	defer cancel()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream = %d", resp.StatusCode)
	}
	waitForLine(t, lines, "event: ready")

	app.pushRolesNudge("someone-else")
	app.pushRolesNudge(kim.ID)
	var event, data string
	deadline := time.After(5 * time.Second)
	for data == "" {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("the stream closed before the nudge")
			}
			if after, found := strings.CutPrefix(line, "event: "); found && event == "" {
				event = after
			} else if after, found := strings.CutPrefix(line, "data: "); found && event != "" {
				data = after
			}
		case <-deadline:
			t.Fatal("no event within 5s")
		}
	}
	if event != pushEventRoles || !strings.Contains(data, `"id":"`+kim.ID+`"`) || !strings.Contains(data, `"kind":"roles"`) {
		t.Fatalf("the first event after ready was %q with %s, want roles for %s", event, data, kim.ID)
	}

	_, cancel2, again := sseStream(t, base, token)
	defer cancel2()
	_ = again.Body.Close()
	if again.StatusCode != http.StatusOK {
		t.Fatalf("after a role nudge the session's next stream = %d, want 200 because a nudge revokes nothing", again.StatusCode)
	}
}

// rolesNudges collects the role nudges app publishes, with the names of
// the roles the nudged user resolves to when each nudge is heard.
type rolesNudges struct {
	mu   sync.Mutex
	seen []heardNudge
}

type heardNudge struct {
	subject string
	roles   []string
}

// listenRolesNudges subscribes to every role nudge of app.
func listenRolesNudges(t *testing.T, app *App) *rolesNudges {
	t.Helper()
	n := &rolesNudges{}
	unsub, err := app.bus.SubscribeCore("straza.push.roles.>", func(subject string, _ []byte) {
		var names []string
		if roles, err := app.resolver.ResolveRoles(context.Background(), strings.TrimPrefix(subject, "straza.push.roles."), time.Now()); err == nil {
			for _, r := range roles {
				names = append(names, r.Name)
			}
		}
		n.mu.Lock()
		n.seen = append(n.seen, heardNudge{subject, names})
		n.mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unsub)
	return n
}

// take answers the nudges heard since the last take. It first publishes a
// nudge of its own and waits for it, so every nudge published before the
// call has been heard.
func (n *rolesNudges) take(t *testing.T, app *App) []heardNudge {
	t.Helper()
	const mark = "straza.push.roles.end-of-step"
	app.pushRolesNudge("end-of-step")
	deadline := time.Now().Add(5 * time.Second)
	for {
		n.mu.Lock()
		if i := slices.IndexFunc(n.seen, func(h heardNudge) bool { return h.subject == mark }); i >= 0 {
			got := slices.Clone(n.seen[:i])
			n.seen = n.seen[i+1:]
			n.mu.Unlock()
			return got
		}
		n.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("the end-of-step nudge was not heard within 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestRoleChangePublishesOneNudge pins where a role nudge is published: the
// server that makes a role change publishes exactly one for the user, for a
// grant and for a removal, after role resolution answers the change. A
// change another server made, which this server only hears, publishes none
// here, because the server that made it already did.
func TestRoleChangePublishesOneNudge(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)
	erin, err := app.store.Users().Create(ctx, store.User{Username: "erin"})
	if err != nil {
		t.Fatal(err)
	}
	auditor, err := app.store.Roles().Create(ctx, store.Role{Name: "auditor"})
	if err != nil {
		t.Fatal(err)
	}
	nudges := listenRolesNudges(t, app)
	erinSubject := rolesPushSubject(erin.ID)

	var created map[string]any
	steps := []struct {
		name     string
		run      func(t *testing.T)
		want     []string
		holds    bool
		checkOne bool
	}{
		{name: "an admin grants the role", run: func(t *testing.T) {
			if code := adminReq(t, http.MethodPost, base+"/v1/admin/assignments", adminTok,
				map[string]any{"subject_kind": "user", "subject_id": erin.ID, "role_id": auditor.ID}, &created); code != http.StatusCreated {
				t.Fatalf("create = %d %v", code, created)
			}
		}, want: []string{erinSubject}, holds: true, checkOne: true},
		{name: "an admin removes the role", run: func(t *testing.T) {
			id, _ := created["id"].(string)
			if code := adminReq(t, http.MethodDelete, base+"/v1/admin/assignments/"+id, adminTok, nil, nil); code != http.StatusOK {
				t.Fatalf("delete = %d", code)
			}
		}, want: []string{erinSubject}, holds: false, checkOne: true},
		{name: "the read of Entra reports a grant", run: func(t *testing.T) {
			app.entraMembership("assigned", "unassigned", true).Changed(ctx, membership.Change{
				AssignmentID: "as-1", RoleID: auditor.ID, UserID: erin.ID, Origin: store.OriginEntra, Action: membership.Assign})
		}, want: []string{erinSubject}},
		{name: "the read of Entra reports a removal", run: func(t *testing.T) {
			app.entraMembership("assigned", "unassigned", true).Changed(ctx, membership.Change{
				AssignmentID: "as-1", RoleID: auditor.ID, UserID: erin.ID, Origin: store.OriginEntra, Action: membership.Unassign})
		}, want: []string{erinSubject}},
		{name: "another server's change is heard", run: func(t *testing.T) {
			if err := app.identityHeard(ctx, erin.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "cached subjects are dropped", run: func(*testing.T) { app.holdingChanged(ctx, erin.ID) }},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			step.run(t)
			got := nudges.take(t, app)
			var subjects []string
			for _, h := range got {
				subjects = append(subjects, h.subject)
			}
			if !slices.Equal(subjects, step.want) {
				t.Fatalf("published %v, want %v", subjects, step.want)
			}
			if step.checkOne && slices.Contains(got[0].roles, "auditor") != step.holds {
				t.Errorf("when the nudge was heard erin resolved to %v, want auditor held = %v, so a session that renews on the nudge gets the change", got[0].roles, step.holds)
			}
		})
	}
}

// listenRolesBroadcasts subscribes to the role broadcast of app, with the
// names of the roles userID resolves to when each broadcast is heard, and
// to the mark that take publishes. One subscription hears both, so the mark
// arrives after every broadcast published before it.
func listenRolesBroadcasts(t *testing.T, app *App, userID string) *rolesNudges {
	t.Helper()
	n := &rolesNudges{}
	mark := rolesPushSubject("end-of-step")
	unsub, err := app.bus.SubscribeCore("straza.push.>", func(subject string, _ []byte) {
		if subject != rolesBroadcastSubject && subject != mark {
			return
		}
		var names []string
		if roles, err := app.resolver.ResolveRoles(context.Background(), userID, time.Now()); err == nil {
			for _, r := range roles {
				names = append(names, r.Name)
			}
		}
		n.mu.Lock()
		n.seen = append(n.seen, heardNudge{subject, names})
		n.mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unsub)
	return n
}

// TestRoleContentChangePublishesOneBroadcast pins the role broadcast: an
// implication added or removed through the admin API publishes exactly one,
// after role resolution answers the change, also when the change reaches a
// holder through another role, and so does a role deleted that no
// implication touches. A publish that changes what no role reaches,
// such as an implication the role reaches already or a policy set, publishes
// none. Kim holds dev, and dev implies reader.
func TestRoleContentChangePublishesOneBroadcast(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	tok, _ := checkinToken(t, app, base)
	dev, _ := app.store.Roles().GetByName(ctx, "dev")
	reader, _ := app.store.Roles().GetByName(ctx, "reader")
	auditor, err := app.store.Roles().Create(ctx, store.Role{Name: "auditor"})
	if err != nil {
		t.Fatal(err)
	}
	retired, err := app.store.Roles().Create(ctx, store.Role{Name: "retired"})
	if err != nil {
		t.Fatal(err)
	}
	heard := listenRolesBroadcasts(t, app, kim.ID)
	implies := func(from, to string) func(t *testing.T) {
		return func(t *testing.T) {
			if code := adminReq(t, http.MethodPost, base+"/v1/admin/roles/"+from+"/implications", tok,
				map[string]any{"implies_role_id": to}, nil); code != http.StatusCreated {
				t.Fatalf("add implication = %d", code)
			}
		}
	}
	const set = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: broadcast-none }
spec:
  rules:
    - {id: r1, tools: [shell.exec], command: {denyPatterns: ["rm -rf *"]}, effect: deny, reason: "no"}
`
	for _, step := range []struct {
		name  string
		run   func(t *testing.T)
		want  int
		role  string
		holds bool
	}{
		{name: "an implication removed", run: func(t *testing.T) {
			if code := adminReq(t, http.MethodDelete, base+"/v1/admin/roles/"+dev.ID+"/implications/"+reader.ID, tok, nil, nil); code != http.StatusOK {
				t.Fatalf("remove implication = %d", code)
			}
		}, want: 1, role: "reader", holds: false},
		{name: "an implication added", run: implies(dev.ID, reader.ID), want: 1, role: "reader", holds: true},
		{name: "an implication that reaches the holder through another role", run: implies(reader.ID, auditor.ID), want: 1, role: "auditor", holds: true},
		{name: "an implication the role reaches already", run: implies(dev.ID, auditor.ID)},
		{name: "a policy set saved", run: func(t *testing.T) { applyPolicyYAML(t, base, tok, set, http.StatusCreated) }},
		{name: "a policy set turned on", run: func(t *testing.T) {
			if code := adminReq(t, http.MethodPost, base+"/v1/admin/policies/broadcast-none/activate", tok,
				map[string]string{"status": "active"}, nil); code != http.StatusOK {
				t.Fatalf("activate = %d", code)
			}
		}},
		{name: "a role with no implication deleted", run: func(t *testing.T) {
			if code := adminReq(t, http.MethodDelete, base+"/v1/admin/roles/"+retired.ID, tok, nil, nil); code != http.StatusOK {
				t.Fatalf("delete role = %d", code)
			}
		}, want: 1, role: "retired", holds: false},
	} {
		t.Run(step.name, func(t *testing.T) {
			step.run(t)
			got := heard.take(t, app)
			if len(got) != step.want {
				t.Fatalf("published %d role broadcasts, want %d", len(got), step.want)
			}
			if step.want > 0 && slices.Contains(got[0].roles, step.role) != step.holds {
				t.Errorf("when the broadcast was heard kim resolved to %v, want %s held = %v, so a session that renews on it gets the change", got[0].roles, step.role, step.holds)
			}
		})
	}
}

// TestRolesBroadcastReachesEveryStream pins the role broadcast on the
// stream of GET /v1/push: an implication added through the admin API
// reaches the stream of every connected user as the event roles, the user
// whose roles it changes and a user it does not touch alike.
func TestRolesBroadcastReachesEveryStream(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	seedGatewayUser(t, app, "erin", "auditor")
	kimToken, _ := checkinToken(t, app, base)
	streams := map[string]string{"kim": kimToken, "erin": sessionToken(t, base, "erin")}
	dev, _ := app.store.Roles().GetByName(ctx, "dev")
	writer, err := app.store.Roles().Create(ctx, store.Role{Name: "writer"})
	if err != nil {
		t.Fatal(err)
	}

	lines := map[string]<-chan string{}
	for name, token := range streams {
		ch, cancel, resp := sseStream(t, base, token)
		t.Cleanup(cancel)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("the stream of %s = %d", name, resp.StatusCode)
		}
		waitForLine(t, ch, "event: ready")
		lines[name] = ch
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/roles/"+dev.ID+"/implications", kimToken,
		map[string]any{"implies_role_id": writer.ID}, nil); code != http.StatusCreated {
		t.Fatalf("add implication = %d", code)
	}
	for name, ch := range lines {
		var event, data string
		deadline := time.After(5 * time.Second)
		for data == "" {
			select {
			case line, ok := <-ch:
				if !ok {
					t.Fatalf("the stream of %s closed before the broadcast", name)
				}
				if after, found := strings.CutPrefix(line, "event: "); found {
					event = after
				} else if after, found := strings.CutPrefix(line, "data: "); found && event != "" {
					data = after
				}
			case <-deadline:
				t.Fatalf("the stream of %s heard no event within 5s", name)
			}
		}
		if event != pushEventRoles || data != `{"kind":"roles"}` {
			t.Errorf("the stream of %s heard %q with %s first, want roles with {\"kind\":\"roles\"}", name, event, data)
		}
	}
}

// TestSCIMMembershipPublishesNudges pins the role nudge on SCIM Group
// writes: each assignment a write starts or ends publishes one nudge for
// its user, and a write that changes nothing publishes none.
func TestSCIMMembershipPublishesNudges(t *testing.T) {
	t.Parallel()
	app, base, token := scimTestSetup(t)
	role, err := app.store.Roles().Create(context.Background(), store.Role{Name: "nudge-dev", Kind: store.RoleKindBusiness})
	if err != nil {
		t.Fatal(err)
	}
	bob := scimCreateUser(t, base, token, `{"userName":"bob@x.io"}`)
	sam := scimCreateUser(t, base, token, `{"userName":"sam@x.io"}`)
	nudges := listenRolesNudges(t, app)
	patch := func(ops string) string {
		return `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":` + ops + `}`
	}
	for _, step := range []struct {
		name, method, body string
		want               []string
	}{
		{"PATCH add", "PATCH", patch(`[{"op":"add","path":"members","value":[{"value":"` + bob + `"}]}]`), []string{bob}},
		{"repeated PATCH add", "PATCH", patch(`[{"op":"add","path":"members","value":[{"value":"` + bob + `"}]}]`), nil},
		{"PATCH remove", "PATCH", patch(`[{"op":"remove","path":"members[value eq \"` + bob + `\"]"}]`), []string{bob}},
		{"PATCH add before the replace", "PATCH", patch(`[{"op":"add","path":"members","value":[{"value":"` + bob + `"}]}]`), []string{bob}},
		{"PUT replace ends bob and starts sam", "PUT",
			`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],"displayName":"nudge-dev","members":[{"value":"` + sam + `"}]}`,
			[]string{bob, sam}},
	} {
		t.Run(step.name, func(t *testing.T) {
			if code, out := scimReq(t, step.method, base, token, "/scim/v2/Groups/"+role.ID, step.body); code != http.StatusOK {
				t.Fatalf("%s = %d %v", step.method, code, out)
			}
			var got, want []string
			for _, h := range nudges.take(t, app) {
				got = append(got, h.subject)
			}
			for _, u := range step.want {
				want = append(want, rolesPushSubject(u))
			}
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("published %v, want %v", got, want)
			}
		})
	}
}
