package server

import (
	"net/http"

	"github.com/strazahq/straza/internal/approval"
)

// selfRequestsScopeRefusal is the 400 of GET /v1/self/requests without a
// scope it knows.
const selfRequestsScopeRefusal = "The scope parameter is missing or unknown, so Straza cannot tell which requests to list. " +
	"Use scope=decidable for the requests you may decide, or scope=mine for the requests you raised."

// handleSelfRequests serves GET /v1/self/requests?scope=decidable|mine
// (requirePersonClient), read with the sign-in session. decidable answers
// the rows GET /v1/approver/pending?scope=decidable answers a device of the
// caller, with no challenge, so nothing can be signed from them. mine
// answers the pending requests the caller raised. Both read the newest 500
// pending records. The read writes nothing: no challenge row, no device's
// last-seen time and no record.
func (a *App) handleSelfRequests(w http.ResponseWriter, r *http.Request, userID string) {
	scope := r.URL.Query().Get("scope")
	var rows []approval.PendingRow
	var err error
	switch scope {
	case "decidable":
		rows, err = a.approval.DecidableRows(r.Context(), userID)
	case "mine":
		rows, err = a.approval.RaisedRows(r.Context(), userID)
	default:
		apiError(w, http.StatusBadRequest, selfRequestsScopeRefusal)
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError,
			"Straza could not read the waiting requests, so it cannot list them. Try again, and tell your administrator if it keeps failing.", err)
		return
	}
	out := approverRows(rows, scope == "decidable")
	a.enrichApproverRows(r.Context(), out)
	writeJSON(w, http.StatusOK, map[string]any{"requests": out})
}
