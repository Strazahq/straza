package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestSCIMRefusedWriteLogsAndRecordsOnce pins that every SCIM write Straza
// refuses with a 4xx or a 501 leaves exactly one WARN line "SCIM write
// refused" and exactly one scim.refused record that names the token that
// acted, the route, the status, the reason and what the write was about,
// while a read Straza refuses and a write that fails with a 5xx leave
// neither. The break-glass account is never named. The rows run in order on
// one server, because later rows use what earlier rows created.
func TestSCIMRefusedWriteLogsAndRecordsOnce(t *testing.T) {
	t.Parallel()
	logger, logs := captureLogger()
	app, base, fs := testAppFaultLog(t, logger)
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	adminTok, _ := checkinToken(t, app, base)
	var minted struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", adminTok,
		map[string]any{"name": "idm-provisioner", "scope": "scim:read,scim:write"}, &minted); code != http.StatusCreated {
		t.Fatalf("mint the SCIM admin API token = %d", code)
	}
	asToken := func(m map[string]any) map[string]any {
		m["action"], m["actor"], m["actorId"], m["actorVia"] = "scim.refused", "idm-provisioner", minted.ID, "api-token"
		return m
	}
	ctx := context.Background()
	glass, err := app.store.Users().GetByUsername(ctx, BreakGlassUsername)
	if err != nil {
		t.Fatalf("the test server holds no break-glass account: %v", err)
	}
	role, err := app.store.Roles().Create(ctx, store.Role{Name: "deployers"})
	if err != nil {
		t.Fatal(err)
	}
	const (
		core   = `"urn:ietf:params:scim:schemas:core:2.0:User"`
		ext    = `urn:straza:params:scim:schemas:extension:2.0:User`
		patch  = `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":`
		entra  = "5f8c2a1e-3b4d-4e6f-9a7b-1c2d3e4f5a6b"
		noUser = "00000000-0000-0000-0000-000000000000"
	)
	agent := func(name, extra string) string {
		return `{"schemas":[` + core + `],"userName":"` + name + `","userType":"agent"` + extra + `}`
	}
	ids := map[string]string{}
	for _, body := range []string{
		`{"schemas":[` + core + `],"userName":"ada"}`,
		agent("sam-sre-agent", `,"`+ext+`":{"entraAgentId":"`+entra+`"}`),
		agent("joe-java-developer", ""),
	} {
		code, resp := scimReq(t, http.MethodPost, base, minted.Token, "/scim/v2/Users", body)
		if code != http.StatusCreated {
			t.Fatalf("create %s = %d %v", body, code, resp)
		}
		ids[resp["userName"].(string)], _ = resp["id"].(string)
	}

	rows := []struct {
		name   string
		method string
		path   string
		body   string
		fault  string
		code   int
		// want is the exact data of the one scim.refused record, or nil when
		// the request must leave no record and no log line.
		want map[string]any
	}{
		{
			name: "a create without userName", method: http.MethodPost, path: "/scim/v2/Users",
			body: `{"schemas":[` + core + `]}`, code: http.StatusBadRequest,
			want: asToken(map[string]any{"route": "POST /scim/v2/Users", "httpStatus": 400,
				"scimType": "invalidValue"}),
		},
		{
			name: "a create over an active userName", method: http.MethodPost, path: "/scim/v2/Users",
			body: `{"schemas":[` + core + `],"userName":"ada"}`, code: http.StatusConflict,
			want: asToken(map[string]any{"route": "POST /scim/v2/Users", "httpStatus": 409,
				"scimType": "uniqueness", "username": "ada"}),
		},
		{
			name: "a PATCH of the read-only lock block", method: http.MethodPatch, path: "/scim/v2/Users/" + ids["ada"],
			body: patch + `[{"op":"replace","path":"locked","value":true}]}`, code: http.StatusBadRequest,
			want: asToken(map[string]any{"route": "PATCH /scim/v2/Users/{id}", "httpStatus": 400, "scimType": "mutability",
				"target": ids["ada"], "user": ids["ada"], "username": "ada"}),
		},
		{
			name: "an entraAgentId another agent holds", method: http.MethodPatch, path: "/scim/v2/Users/" + ids["joe-java-developer"],
			body: patch + `[{"op":"replace","path":"entraAgentId","value":"` + entra + `"}]}`, code: http.StatusConflict,
			want: asToken(map[string]any{"route": "PATCH /scim/v2/Users/{id}", "httpStatus": 409, "scimType": "uniqueness",
				"target": ids["joe-java-developer"], "user": ids["joe-java-developer"], "username": "joe-java-developer"}),
		},
		{
			name: "a PUT of a user Straza does not hold", method: http.MethodPut, path: "/scim/v2/Users/" + noUser,
			body: `{"schemas":[` + core + `],"userName":"ghost"}`, code: http.StatusNotFound,
			want: asToken(map[string]any{"route": "PUT /scim/v2/Users/{id}", "httpStatus": 404, "target": noUser}),
		},
		{
			name: "a PATCH of the break-glass account", method: http.MethodPatch, path: "/scim/v2/Users/" + glass.ID,
			body: patch + `[{"op":"replace","path":"active","value":false}]}`, code: http.StatusNotFound,
			want: asToken(map[string]any{"route": "PATCH /scim/v2/Users/{id}", "httpStatus": 404, "target": glass.ID}),
		},
		{
			name: "a group create", method: http.MethodPost, path: "/scim/v2/Groups",
			body: `{"displayName":"new-role"}`, code: http.StatusNotImplemented,
			want: asToken(map[string]any{"route": "POST /scim/v2/Groups", "httpStatus": 501}),
		},
		{
			name: "a member Straza does not hold", method: http.MethodPatch, path: "/scim/v2/Groups/" + role.ID,
			body: patch + `[{"op":"add","path":"members","value":[{"value":"` + noUser + `"}]}]}`, code: http.StatusBadRequest,
			want: asToken(map[string]any{"route": "PATCH /scim/v2/Groups/{id}", "httpStatus": 400, "scimType": "invalidValue",
				"target": role.ID, "role": "deployers"}),
		},
		{
			name: "a read with a filter Straza refuses", method: http.MethodGet, path: `/scim/v2/Users?filter=userName%20co%20%22a%22`,
			code: http.StatusBadRequest,
		},
		{
			name: "a write the database failed", method: http.MethodPatch, path: "/scim/v2/Users/" + ids["ada"],
			body: patch + `[{"op":"replace","path":"title","value":"Engineer"}]}`, fault: "users", code: http.StatusInternalServerError,
		},
	}
	for _, row := range rows {
		admins := len(adminAuditEvents(t, app))
		logs.Reset()
		if row.fault != "" {
			fs.arm(row.fault, errors.New("database unreachable"))
		}
		code, resp := scimReq(t, row.method, base, minted.Token, row.path, row.body)
		fs.disarm()
		if code != row.code {
			t.Fatalf("%s: status %d, want %d (%v)", row.name, code, row.code, resp)
		}

		var lines []string
		for _, line := range strings.Split(logs.String(), "\n") {
			if strings.Contains(line, `msg="SCIM write refused"`) {
				lines = append(lines, line)
			}
		}
		events := adminAuditEvents(t, app)[admins:]
		if row.want == nil {
			if len(lines) != 0 || len(events) != 0 {
				t.Errorf("%s: logged %v and chained %v, want neither", row.name, lines, events)
			}
			continue
		}
		// The record's reason is the detail the client was told, word for word.
		detail, _ := resp["detail"].(string)
		if detail == "" {
			t.Fatalf("%s: the answer carries no detail: %v", row.name, resp)
		}
		row.want["reason"] = detail
		if len(lines) != 1 {
			t.Errorf("%s: %d refusal lines, want exactly one:\n%s", row.name, len(lines), logs.String())
		} else {
			for _, want := range []string{"level=WARN", "status=" + jsonNumber(row.want["httpStatus"]),
				`route="` + row.want["route"].(string) + `"`, "actor=idm-provisioner", "reason="} {
				if !strings.Contains(lines[0], want) {
					t.Errorf("%s: refusal line lacks %s: %s", row.name, want, lines[0])
				}
			}
			if strings.Contains(lines[0], BreakGlassUsername) {
				t.Errorf("%s: refusal line names the break-glass account: %s", row.name, lines[0])
			}
		}
		if len(events) != 1 {
			t.Errorf("%s: chained %d admin records, want exactly one: %v", row.name, len(events), events)
			continue
		}
		gotJSON, _ := json.Marshal(events[0])
		wantJSON, _ := json.Marshal(row.want)
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("%s: record\n got %s\nwant %s", row.name, gotJSON, wantJSON)
		}
	}
}

// jsonNumber renders an int the way the log line writes it.
func jsonNumber(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
