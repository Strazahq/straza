package server

import (
	"fmt"
	"math"
	"strconv"
)

// userRateFactor is how many times what one session may spend on a server
// all sessions of one user may spend on it together.
const userRateFactor = 5

// userRateSentence is the refusal of a call past the ceiling of a user's
// sessions for the server it names.
const userRateSentence = "Straza: rate limit exceeded for the MCP server %q: all your sessions together may make %s calls a second. " +
	"Wait a few seconds, then try the call once more. If it is denied again, tell the user."

// userCeiling answers the rate and the burst all sessions of one user share
// on a server whose rate for one session is rps: userRateFactor times the
// session's rate and its burst, which is one call for a rate under 1.
func userCeiling(rps float64) (rate, burst float64) {
	return rps * userRateFactor, max(rps, 1) * userRateFactor
}

// userRateRefusal takes one call to the server app from the ceiling all
// sessions of the user userID share there, and answers the refusal when the
// ceiling is spent, "" otherwise. A server with no rate has no ceiling. The
// count is by the token's subject, so a caller that opens more sessions
// gets no more calls. The gateway asks only after the session's own bucket
// let the call through, so one session cannot spend more of the ceiling
// than its own rate, and never for the drafting tools, which keep the two
// rates of their own.
func (a *App) userRateRefusal(userID, app string, rps float64) string {
	rate, burst := userCeiling(rps)
	if a.gateway.limiter.AllowBurst("user|"+userID+"|"+app, rate, burst) {
		return ""
	}
	return fmt.Sprintf(userRateSentence, app, strconv.FormatFloat(math.Round(rate*1000)/1000, 'f', -1, 64))
}
