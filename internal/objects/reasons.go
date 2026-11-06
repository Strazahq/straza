package objects

import (
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/redact"
)

// The reason sentences a failed listing stores and every surface shows as
// they are. None carries a secret, and none quotes the server beyond the
// first line of a tool's own error text.

func reasonNoCredential(app, plural string) string {
	return fmt.Sprintf("%[1]s has no listing credential, so Straza cannot list its %[2]s. Set one on its server page under Server authentication, or with strazactl apps secret set %[1]s --listing. Then press List again on its %[2]s tab, or run strazactl apps objects refresh %[1]s.", app, plural)
}

func reasonNotRunning(app, tool string) string {
	return fmt.Sprintf("%s is not running, so Straza could not call %s. Start it, then refresh.", app, tool)
}

func reasonToolsUnread(app, tool, plural string) string {
	return fmt.Sprintf("Straza has not read %[1]s's tool list yet, so it cannot check %[2]s before it lists %[1]s's %[3]s. It tries again in about a minute. If %[1]s stays degraded, read why with strazactl apps show %[1]s.", app, tool, plural)
}

func reasonUnreachable(app, tool string) string {
	return fmt.Sprintf("Straza could not reach %[1]s to call %[2]s. Check %[1]s's logs on its Activity tab, then refresh.", app, tool)
}

// reasonRefused is the sentence of a listing call the server answered with
// HTTP status in place of running it. A 401 or 403 to a call that carried
// the listing credential means the server does not hold that value.
func reasonRefused(app, tool, plural string, status int, withCredential bool) string {
	code := fmt.Sprintf("HTTP %d %s", status, http.StatusText(status))
	if withCredential && (status == http.StatusUnauthorized || status == http.StatusForbidden) {
		return fmt.Sprintf("%[1]s refused the listing credential with %[2]s, so Straza could not list its %[3]s. %[1]s's own config must hold the value Straza stores: put it there and restart %[1]s, or store a new value under Server authentication on its server page and give %[1]s that one. Then press List again on its %[3]s tab, or run strazactl apps objects refresh %[1]s.", app, code, plural)
	}
	return fmt.Sprintf("%[1]s answered the call to %[2]s with %[3]s, so Straza could not list its %[4]s. Check %[1]s's logs on its Activity tab, then refresh.", app, tool, code, plural)
}

func reasonNoTool(app, tool, plural string) string {
	return fmt.Sprintf("%[1]s has no tool named %[2]s, so Straza cannot list its %[3]s. Name the listing tool %[1]s has in straza.objects.list.tool, or ask the server's author to add %[2]s.", app, tool, plural)
}

func reasonNotReadOnly(tool, plural string) string {
	return fmt.Sprintf("%s is not marked read-only, so Straza will not call it to list %s. The server's author must set readOnlyHint on it.", tool, plural)
}

func reasonToolError(app, tool string, res *mcp.CallToolResult) string {
	return fmt.Sprintf("%s answered an error: %s. Check %s's logs on its Activity tab.", tool, shortText(res), app)
}

func reasonShape(tool string) string {
	return fmt.Sprintf("%s answered without an objects list in its structured content. The server's author must return {objects, nextCursor} as the Straza object marks describe.", tool)
}

func reasonCursorLoop(tool string) string {
	return fmt.Sprintf("%s answered with the cursor it was called with as nextCursor, so the listing would never end. The server's author must return a new nextCursor, or none on the last page.", tool)
}

// reasonLimit is the sentence of a listing refused for one item or for its
// size: what the tool returned, and that the last good list stays.
func reasonLimit(tool, what string) string {
	return fmt.Sprintf("%s returned %s, so the listing was refused as a whole and the last good list stays.", tool, what)
}

func reasonPageTimeout(tool string, page int) string {
	return fmt.Sprintf("%s did not answer within 30 seconds on page %d.", tool, page)
}

const reasonListingTimeout = "The listing took longer than 5 minutes."

// shortText is the first line of the first text of a tool's error answer,
// at most 160 bytes with credential shapes masked, or "no message".
func shortText(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		t, ok := c.(*mcp.TextContent)
		if !ok {
			continue
		}
		line, _, _ := strings.Cut(strings.TrimSpace(t.Text), "\n")
		line = strings.TrimRight(strings.TrimSpace(redact.Redact(line)), ".")
		if len(line) > 160 {
			line = line[:160]
			for !utf8.ValidString(line) {
				line = line[:len(line)-1]
			}
			line += "…"
		}
		if line != "" {
			return line
		}
	}
	return "no message"
}
