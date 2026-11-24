package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// wantPersonClientHarnessRefusal is the 403 sentence a coding harness's
// session reads on every route that takes a person's client, formatted
// with the harness name and the surfaces hint.
const wantPersonClientHarnessRefusal = "this session belongs to the coding harness %q, and a coding harness cannot decide a request, " +
	"enroll a device that decides, or read or remove a person's approver devices and waiting requests. Use %s"

// selfCaller is one kind of caller that the routes of a person's own
// approval data meet. want is 0 for a caller the check lets through and
// the status of the refusal otherwise. says, when set, is the refusal's
// exact sentence.
type selfCaller struct {
	name   string
	bearer string
	want   int
	says   string
}

// selfCallers builds lin, a plain person, and every kind of caller: the
// three credentials a person holds in a browser, and the refused ones. It
// answers lin and the callers.
func selfCallers(t *testing.T, app *App, base string) (store.User, []selfCaller) {
	t.Helper()
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminSession, _ := checkinToken(t, app, base)
	_, apiToken := mintConnectToken(t, base, adminSession, "iga-pull", "approvals:write")

	lin := mkHuman(t, app, "lin")
	agent, err := app.store.Users().Create(ctx, store.User{Username: "lin-bot", UserType: store.UserTypeAgent, Sponsor: "lin"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := app.store.Users().Create(ctx, store.User{Username: "ci-runner", UserType: store.UserTypeService})
	if err != nil {
		t.Fatal(err)
	}
	idToken, err := app.tokens.MintIDToken(lin.ID, "straza", 10*time.Minute, lin.Username, lin.Email)
	if err != nil {
		t.Fatal(err)
	}
	_, console := storedSession(t, app, lin.ID, "", "console")
	_, selfService := storedSession(t, app, lin.ID, "", "self-service")
	_, harness := storedSession(t, app, lin.ID, "", "claude-code")
	_, agentSes := storedSession(t, app, agent.ID, "", "claude-code")
	_, serviceSes := storedSession(t, app, service.ID, "", "exec-wrapper")
	_, _, approverTok := enrollApprover(t, app, lin.ID)

	return lin, []selfCaller{
		{"a person's console session", console, 0, ""},
		{"a person's self-service session", selfService, 0, ""},
		{"a person's ID token", idToken, 0, ""},
		{"no session", "", http.StatusUnauthorized, ""},
		{"a person's coding harness session", harness, http.StatusForbidden,
			fmt.Sprintf(wantPersonClientHarnessRefusal, "claude-code", app.approvalSurfacesHint())},
		{"an AI agent's session", agentSes, http.StatusForbidden, ""},
		{"a service account's session", serviceSes, http.StatusForbidden, ""},
		{"an admin API token", apiToken, http.StatusUnauthorized, ""},
		{"an approver device's own token", approverTok, http.StatusUnauthorized, ""},
	}
}

// selfRecordSubjects are the subjects a refusal or a removal could write
// a record on.
var selfRecordSubjects = map[string]bool{
	"straza.audit.authn": true, "straza.audit.admin": true,
	"straza.audit.identity": true, "straza.identity.updated": true,
}

// recordsDuring answers each record that call adds to the outbox on
// selfRecordSubjects, oldest first, as its subject and its data.
func recordsDuring(t *testing.T, app *App, call func()) []string {
	t.Helper()
	before, err := app.store.Outbox().ListRecent(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, row := range before {
		seen[row.ID] = true
	}
	call()
	after, err := app.store.Outbox().ListRecent(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := len(after) - 1; i >= 0; i-- {
		row := after[i]
		if seen[row.ID] || !selfRecordSubjects[row.Subject] {
			continue
		}
		var ce struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(row.CE), &ce); err != nil {
			t.Fatalf("bad CE in outbox: %v", err)
		}
		out = append(out, row.Subject+" "+string(mustJSON(ce.Data)))
	}
	return out
}

// TestSelfRoutesCallerTable runs every kind of caller against every route
// of a person's own approval data. A person's console session, self-service
// session and ID token pass the check. No session, a coding harness's
// session, an AI agent's or a service account's session, an admin API token
// and an approver device's own token are refused with the status the
// table names and a sentence, the coding harness with the sentence that
// names these routes, and each refusal writes exactly the records
// the same caller's refused call of the self enroll-token route writes. A
// refused removal leaves the device it named in place.
func TestSelfRoutesCallerTable(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	lin, callers := selfCallers(t, app, base)
	_, linDevice, _ := enrollApprover(t, app, lin.ID)

	routes := []struct {
		method, path string
		// passed is the status a caller the check lets through reads, and
		// passedSays the sentence of that answer, "" for a 200.
		passed     int
		passedSays string
		// refusedOnly marks a route only refused callers are sent to.
		refusedOnly bool
	}{
		{http.MethodGet, "/v1/self/devices", http.StatusOK, "", false},
		{http.MethodDelete, "/v1/self/devices/apd_nobody", http.StatusNotFound, selfDeviceMissing, false},
		{http.MethodDelete, "/v1/self/devices/" + linDevice, 0, "", true},
		{http.MethodGet, "/v1/self/requests?scope=decidable", http.StatusOK, "", false},
		{http.MethodGet, "/v1/self/requests?scope=mine", http.StatusOK, "", false},
		{http.MethodGet, "/v1/self/requests?scope=all", http.StatusBadRequest, selfRequestsScopeRefusal, false},
	}
	for _, c := range callers {
		var enrollRecords []string
		if c.want != 0 {
			enrollRecords = recordsDuring(t, app, func() {
				adminBytes(t, http.MethodPost, base+"/v1/approvals/self/enroll-token", c.bearer, "application/json", []byte(`{"channel":"browser"}`))
			})
		}
		for _, rt := range routes {
			if rt.refusedOnly && c.want == 0 {
				continue
			}
			var code int
			var raw []byte
			got := recordsDuring(t, app, func() {
				code, raw, _ = adminBytes(t, rt.method, base+rt.path, c.bearer, "", nil)
			})
			var body struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(raw, &body)
			if c.want == 0 {
				if code != rt.passed || body.Error != rt.passedSays {
					t.Errorf("%s: %s %s = %d %s, want %d %q", c.name, rt.method, rt.path, code, raw, rt.passed, rt.passedSays)
				}
				continue
			}
			if code != c.want || body.Error == "" || (c.says != "" && body.Error != c.says) {
				t.Errorf("%s: %s %s = %d %s, want %d with a sentence %q", c.name, rt.method, rt.path, code, raw, c.want, c.says)
			}
			if string(mustJSON(got)) != string(mustJSON(enrollRecords)) {
				t.Errorf("%s: %s %s wrote records\n got %v\nwant %v, as the enroll-token route", c.name, rt.method, rt.path, got, enrollRecords)
			}
		}
	}
	if _, err := app.store.Approvers().GetDevice(context.Background(), linDevice); err != nil {
		t.Errorf("a refused removal took lin's device: %v", err)
	}
}
