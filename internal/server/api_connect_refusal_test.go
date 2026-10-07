package server

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// TestConnectTokenPasteAnswersTheDialRefusal pins the token paste against a
// server whose address strazad does not dial: the answer is the refusal's
// own sentence, which names the administrator's next step, and not the
// frame that blames the token, since the token was never tried.
func TestConnectTokenPasteAnswersTheDialRefusal(t *testing.T) {
	app, base := testApp(t, func(c *config.Config) { c.Apps.AllowLoopbackUpstreams = false })
	up := startGatewayUpstream(t)
	seedGatewayUser(t, app, "alice", "dev")
	mf, err := managerParse(t, fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: tokapp}
server: {name: straza.test/tokapp, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %q}
  credential:
    kind: token
    agents: sponsor
    inject: {as: header, name: Authorization, template: "Bearer {{secret}}"}
  exposure:
    tools: ["*"]
`, up.URL))
	if err != nil {
		t.Fatal(err)
	}
	row, err := app.manager.Install(context.Background(), mf, store.AppSourceAPI)
	if err != nil {
		t.Fatal(err)
	}
	catReach(t, app, "dev", row, `["*"]`)
	var got map[string]any
	body := map[string]any{"token": "tok-x", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	code := adminReq(t, http.MethodPost, base+"/v1/connect/tokapp", sessionToken(t, base, "alice"), body, &got)
	want := "Straza does not dial 127.0.0.1 for the server tokapp because it is a loopback address on strazad's own host. " +
		"An administrator publishes an address other machines can reach, or sets apps.allowLoopbackUpstreams when strazad runs beside the server on purpose."
	if code != http.StatusBadGateway || got["error"] != want {
		t.Fatalf("paste = %d %v\nwant 502 %q", code, got, want)
	}
}
