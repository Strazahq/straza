package server

import "net/http"

// packsRetired is the answer of every knowledge pack route. The routes stay
// until the next major version, so an older strazactl or script reads this
// sentence instead of a bare 404.
const packsRetired = "Knowledge packs were removed from Straza, so this route does nothing. Remove the call from your script or tool. Nothing else needs to change."

// handlePacksRetired answers 410 Gone with packsRetired on each of the five
// knowledge pack routes, after the admin gate. It reads no body and reads or
// writes nothing in the store.
func (a *App) handlePacksRetired(w http.ResponseWriter, _ *http.Request) {
	apiError(w, http.StatusGone, packsRetired)
}
