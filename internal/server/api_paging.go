package server

import (
	"encoding/base64"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/strazahq/straza/internal/store"
)

// Admin-list pagination. Opt-in and non-breaking: a request carrying
// ?limit= or ?cursor= answers the {items, next_cursor} envelope (the
// approver-history shape); a bare request keeps the legacy response
// byte-identical, so existing consumers (the midPoint pull connector,
// current strazactl) are untouched.
// Cursors are opaque tokens: the id of the row a page continues after
// (parseHistoryCursor's form) or, under a sort, that row's sort value with
// the key and direction it was read in, so a stale or foreign cursor is a
// loud 400 and never a page in another order. Limit follows the history
// precedent: non-numeric or missing falls to the default, over-max clamps.
const (
	adminPageDefaultLimit = 100
	adminPageMaxLimit     = 500
)

// sortKeys names the keys one list offers and, per key, whether it opens on
// the newest or largest value (true) or ascending (false) when no order is
// given.
type sortKeys map[string]bool

type pageParams struct {
	limit  int
	before string     // the id of the row the page continues after ("" = the first page)
	value  string     // that row's sort value, "" under the default order
	sort   store.Sort // the order the page is read in
	paged  bool
}

// parsePageParams reads limit and cursor for a list that offers no sort. A
// sort or order parameter on such a list is refused, since a parameter
// that silently did nothing would lie about the order. On any refusal the
// 400 is written here and ok is false.
func parsePageParams(w http.ResponseWriter, r *http.Request) (pageParams, bool) {
	return parseSortedPage(w, r, nil)
}

// parseSortedPage reads limit, cursor, sort and order. Sorting needs the
// paged lane, like the filters; the key must be one the list offers; the
// order defaults per key and must be asc or desc when given; and a cursor
// minted under another key or direction is stale.
func parseSortedPage(w http.ResponseWriter, r *http.Request, keys sortKeys) (pageParams, bool) {
	q := r.URL.Query()
	_, hasLimit := q["limit"]
	_, hasCursor := q["cursor"]
	p := pageParams{limit: adminPageDefaultLimit, paged: hasLimit || hasCursor}
	key, order := q.Get("sort"), q.Get("order")
	if !p.paged {
		if key != "" || order != "" {
			apiError(w, http.StatusBadRequest, "sort and order require pagination: add limit=")
			return p, false
		}
		return p, true
	}
	if keys == nil && (key != "" || order != "") {
		apiError(w, http.StatusBadRequest, "this list has one order and takes no sort")
		return p, false
	}
	if keys != nil {
		desc, ok := keys[key]
		if !ok {
			apiError(w, http.StatusBadRequest, "sort must be one of "+strings.Join(keyNames(keys), ", "))
			return p, false
		}
		switch order {
		case "":
			p.sort = store.Sort{Key: key, Asc: !desc}
		case "asc", "desc":
			p.sort = store.Sort{Key: key, Asc: order == "asc"}
		default:
			apiError(w, http.StatusBadRequest, "order must be asc or desc")
			return p, false
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			p.limit = n
		}
	}
	if p.limit > adminPageMaxLimit {
		p.limit = adminPageMaxLimit
	}
	if raw := q.Get("cursor"); raw != "" {
		c, ok := parseSortCursor(raw)
		if !ok || c.sort != p.sort {
			apiError(w, http.StatusBadRequest, "invalid or stale cursor: re-fetch from the start")
			return p, false
		}
		p.before, p.value = c.id, c.value
	}
	return p, true
}

// keyNames lists a list's sort keys for the refusal sentence; the default
// key is spelled by omission and left out.
func keyNames(keys sortKeys) []string {
	var names []string
	for k := range keys {
		if k != "" {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	return names
}

// sortCursor is a decoded continuation token.
type sortCursor struct {
	sort  store.Sort
	value string
	id    string
}

// pageCursor mints the token for the row a page continues after: the
// history form under the default order, so cursors minted before sorting
// existed keep working, and the sorted form, which names the key and the
// direction as well, under a sort. id "" is the last page.
func pageCursor(p pageParams, value, id string) string {
	if id == "" {
		return ""
	}
	if p.sort == (store.Sort{}) {
		return historyCursor(id)
	}
	dir := "desc"
	if p.sort.Asc {
		dir = "asc"
	}
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join([]string{"2", p.sort.Key, dir, value, id}, "\x00")))
}

// parseSortCursor decodes either cursor form; a malformed token is not ok.
func parseSortCursor(cursor string) (sortCursor, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return sortCursor{}, false
	}
	parts := strings.Split(string(raw), "\x00")
	switch {
	case len(parts) == 2 && parts[0] == "1" && parts[1] != "":
		return sortCursor{id: parts[1]}, true
	case len(parts) == 5 && parts[0] == "2" && (parts[2] == "asc" || parts[2] == "desc") && parts[4] != "":
		return sortCursor{sort: store.Sort{Key: parts[1], Asc: parts[2] == "asc"}, value: parts[3], id: parts[4]}, true
	}
	return sortCursor{}, false
}

// writePage answers the paged envelope under the default order. nextID "" =
// the last page.
func writePage(w http.ResponseWriter, items any, nextID string) {
	writePageCursor(w, items, historyCursor(nextID))
}

// writePageCursor answers the envelope with a cursor already minted.
func writePageCursor(w http.ResponseWriter, items any, cursor string) {
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": cursor})
}

// pageAfter cuts one page from an already-sorted in-memory slice (the
// settings-map lists), starting after the row whose key equals the cursor.
// A cursor naming no row (revoked between pages) is stale: ok=false, the
// caller 400s like a malformed cursor.
func pageAfter[T any](rows []T, key func(T) string, before string, limit int) (page []T, next string, ok bool) {
	start := 0
	if before != "" {
		found := false
		for i := range rows {
			if key(rows[i]) == before {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return nil, "", false
		}
	}
	end := start + limit
	if end > len(rows) {
		end = len(rows)
	}
	page = rows[start:end]
	if end < len(rows) && len(page) > 0 {
		next = key(page[len(page)-1])
	}
	return page, next, true
}
