package store

import (
	"errors"
	"time"
)

// Sort names the key a paged list is ordered by and the direction. The zero
// value is the list's default order, the newest row first by its UUIDv7 id;
// Asc reverses whichever key is named.
type Sort struct {
	Key string
	Asc bool
}

// Cursor is the keyset position a page continues after: the id of the last
// row of the previous page and, for a sort on another key, that row's sort
// value as the handler minted it. An empty ID is the first page.
type Cursor struct {
	Value string
	ID    string
}

// ErrBadSort is returned for a sort key the list does not offer, and
// ErrBadCursor for a cursor whose value does not fit the sort key.
var (
	ErrBadSort   = errors.New("store: unknown sort key")
	ErrBadCursor = errors.New("store: cursor does not fit the sort")
)

// sortKey is one sortable key of a paged list. expr writes the SQL the rows
// are ordered by, appending any argument it needs through ph, so a key
// that embeds a placeholder stays legal under the one-use placeholder rule.
// isTime marks a key whose cursor value is a timestamp, which travels
// through tArg so both dialects compare it the way they store it.
type sortKey struct {
	expr   func(ph func(any) string) string
	isTime bool
}

func column(name string) sortKey {
	return sortKey{expr: func(func(any) string) string { return name }}
}

// keyset returns the predicate that continues after the cursor row and the
// ORDER BY clause of a sorted page, in that order, so the placeholders they
// append stay ascending. Ties on the sort value break on the id, newest
// first, so a walk under any key visits each row exactly once. The id key
// itself keys on the id alone, the original keyset shape.
func (s *sqlStore) keyset(k sortKey, idCol string, srt Sort, c Cursor, ph func(any) string) (where, order string, err error) {
	dir, cmp := " DESC", " < "
	if srt.Asc {
		dir, cmp = " ASC", " > "
	}
	if c.ID != "" {
		if k.expr(func(any) string { return "" }) == idCol {
			where = " AND " + idCol + cmp + ph(c.ID)
		} else {
			var v any = c.Value
			if k.isTime {
				t, perr := time.Parse(time.RFC3339Nano, c.Value)
				if perr != nil {
					return "", "", ErrBadCursor
				}
				v = s.tArg(t)
			}
			where = " AND (" + k.expr(ph) + cmp + ph(v) + " OR (" + k.expr(ph) + " = " + ph(v) + " AND " + idCol + " < " + ph(c.ID) + "))"
		}
	}
	order = " ORDER BY " + k.expr(ph) + dir + ", " + idCol + " DESC"
	return where, order, nil
}
