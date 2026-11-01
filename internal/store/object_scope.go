package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ObjectScope is a binding's stored covers line: every object of the
// server's pinned type, or the listed objects by id. Objects are sorted by
// value when stored.
type ObjectScope struct {
	Every   bool            `json:"every,omitempty"`
	Objects []CoveredObject `json:"objects,omitempty"`
}

// CoveredObject is one object a binding covers, by id, with the value it
// had when published.
type CoveredObject struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

// Key answers the scope key every binding comparison reads: "" for no
// covers line, "every", or "objects:" and the sorted ids joined by NUL. A
// value is not read, so renaming an object changes no key.
func (s *ObjectScope) Key() string {
	switch {
	case s == nil:
		return ""
	case s.Every:
		return "every"
	}
	ids := make([]string, len(s.Objects))
	for i, o := range s.Objects {
		ids[i] = o.ID
	}
	slices.Sort(ids)
	return "objects:" + strings.Join(ids, "\x00")
}

// check answers why s is not a covers line the gate can read: exactly one
// of every and objects, and an id on every object.
func (s *ObjectScope) check() error {
	if s.Every == (len(s.Objects) > 0) {
		return errors.New("it must cover every object or name at least one, not both")
	}
	for _, o := range s.Objects {
		if o.ID == "" {
			return fmt.Errorf("the object %q has no id", o.Value)
		}
	}
	return nil
}

// encodeScope answers the object_scope column value of s: nil, which
// stores NULL, for no covers line, or the JSON text with the objects sorted
// by value. It refuses a form the gate cannot read.
func encodeScope(s *ObjectScope) (any, error) {
	if s == nil {
		return nil, nil
	}
	if err := s.check(); err != nil {
		return nil, fmt.Errorf("store: the covers line cannot be stored: %w", err)
	}
	// Marshal cannot fail on a bool and strings.
	text, _ := json.Marshal(canonicalScope(s))
	return string(text), nil
}

// canonicalScope answers a copy of s in its stored form, the objects
// sorted by value, and nil for nil.
func canonicalScope(s *ObjectScope) *ObjectScope {
	if s == nil {
		return nil
	}
	out := &ObjectScope{Every: s.Every, Objects: slices.Clone(s.Objects)}
	slices.SortFunc(out.Objects, func(a, b CoveredObject) int { return strings.Compare(a.Value, b.Value) })
	return out
}

// decodeScope answers the covers line an object_scope column holds, nil
// for NULL. A stored text the gate cannot read answers nil and the fault
// sentence for row, which names the access row, never a scope.
func decodeScope(row string, ns sql.NullString) (*ObjectScope, string) {
	if !ns.Valid {
		return nil, ""
	}
	var s ObjectScope
	err := json.Unmarshal([]byte(ns.String), &s)
	if err == nil {
		err = s.check()
	}
	if err != nil {
		return nil, fmt.Sprintf("Straza leaves %s out, because its covers line cannot be read: %v. The row grants nothing until it is fixed. "+
			"Only a write outside Straza leaves such a line, so remove the role's access and give it again, or restore the row from a backup.", row, err)
	}
	return &s, ""
}

// pinnedType answers the object type that manifest, a stored manifest's
// JSON, pins: straza.objects.type, "" when it pins none or does not decode.
func pinnedType(manifest string) string {
	var mf struct {
		Straza struct {
			Objects *struct {
				Type string `json:"type"`
			} `json:"objects"`
		} `json:"straza"`
	}
	if json.Unmarshal([]byte(manifest), &mf) != nil || mf.Straza.Objects == nil {
		return ""
	}
	return mf.Straza.Objects.Type
}

// pinScopes keeps the covers lines of the live server app's access rows in
// step with the publish's change to its pinned type, inside the publish's
// transaction. The first pin gives every row without a covers line covers
// every, which is the reach it had, so no decision changes. An unpin
// clears every covers line it can read, because a server that pins no type
// has nothing a covers line could name. A line it cannot read stays, so
// that row keeps granting nothing. A type change keeps the lines, and the
// draft check refuses one while a row names objects. The live reads of the
// plan's roles on app take the new lines, so a role put whose row the
// draft keeps, planned with the line the pin gives it, keeps its row. It
// answers the ids of the roles with a row on app whenever the type changes,
// since each one's SCIM group renders the type and the line.
func (p *publishTx) pinScopes(ctx context.Context, app *App, after string) ([]string, error) {
	was, is := pinnedType(app.Manifest), pinnedType(after)
	if was == is {
		return nil, nil
	}
	roles, err := p.rolesOn(ctx, app.ID)
	if err != nil {
		return nil, err
	}
	var take func(*ObjectScope) (*ObjectScope, bool)
	switch {
	case was == "":
		if _, err := p.tx.ExecContext(ctx, p.r.s.q(`UPDATE tool_bindings SET object_scope = '{"every":true}'
			WHERE app_id = $1 AND object_scope IS NULL`), app.ID); err != nil {
			return nil, p.r.s.mapErr(err)
		}
		take = func(s *ObjectScope) (*ObjectScope, bool) { return &ObjectScope{Every: true}, s == nil }
	case is == "":
		ids, err := p.readableScopes(ctx, app.ID)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if _, err := p.tx.ExecContext(ctx, p.r.s.q(`UPDATE tool_bindings SET object_scope = NULL WHERE id = $1`), id); err != nil {
				return nil, p.r.s.mapErr(err)
			}
		}
		take = func(*ObjectScope) (*ObjectScope, bool) { return nil, true }
	default:
		return roles, nil
	}
	for i := range p.live {
		lo := &p.live[i]
		if lo.binding == nil || lo.binding.AppID != app.ID || lo.binding.ScopeFault != "" {
			continue
		}
		if s, ok := take(lo.scope); ok {
			lo.scope, lo.binding.Scope = s, s
		}
	}
	return roles, nil
}

// rolesOn answers the ids of the roles with an access row on appID, in id
// order, closing the rows before the next statement.
func (p *publishTx) rolesOn(ctx context.Context, appID string) ([]string, error) {
	rows, err := p.tx.QueryContext(ctx, p.r.s.q(`SELECT DISTINCT role_id FROM tool_bindings WHERE app_id = $1 ORDER BY role_id`), appID)
	if err != nil {
		return nil, p.r.s.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// readableScopes answers the ids of the access rows of appID whose covers
// line is set and reads, closing the rows before the next statement. The
// check runs in Go, because neither dialect can tell in SQL which line
// the gate reads.
func (p *publishTx) readableScopes(ctx context.Context, appID string) ([]string, error) {
	rows, err := p.tx.QueryContext(ctx, p.r.s.q(`SELECT id, object_scope FROM tool_bindings
		WHERE app_id = $1 AND object_scope IS NOT NULL`), appID)
	if err != nil {
		return nil, p.r.s.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		var scope sql.NullString
		if err := rows.Scan(&id, &scope); err != nil {
			return nil, err
		}
		if _, fault := decodeScope(id, scope); fault == "" {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}
