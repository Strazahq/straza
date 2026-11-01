package server

import (
	"cmp"
	"context"
	"maps"
	"net/http"
	"slices"
	"strconv"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/objects"
	"github.com/strazahq/straza/internal/store"
)

// objectDraftsRefusal is the 500 sentence of a server's object list whose
// open drafts could not be read.
const objectDraftsRefusal = "Straza could not read the open drafts that change this server's roles, so it cannot show the object list. " +
	"Try again, and read the strazad log if it keeps failing."

// objectDraft is one open draft that would change which roles cover an
// object: the draft's id, the role, and whether publishing the draft adds
// the role to the object or drops it.
type objectDraft struct {
	Draft  string `json:"draft"`
	Role   string `json:"role"`
	Change string `json:"change"`
}

// coverSet is what one covers line reaches on a server: every object, or
// the objects whose ids it holds.
type coverSet struct {
	every bool
	ids   map[string]bool
}

// has reports whether s reaches the object with the id.
func (s coverSet) has(id string) bool { return s.every || s.ids[id] }

// equal reports whether s and o reach the same objects.
func (s coverSet) equal(o coverSet) bool {
	return s.every == o.every && (s.every || maps.Equal(s.ids, o.ids))
}

// coversChange is one role item of an open draft that changes what the role
// covers on one server: the draft's id, the role, what the role covers
// there now and after the draft, and the item as the store holds it.
type coversChange struct {
	draft      int64
	role       string
	live, next coverSet
	item       store.DraftItemRow
}

// objectDrafts answers, for the server app whose object list is list, a
// function that names for an object's id the open drafts that would add or
// drop a role covering it, as draftsOn says, each in a draft the caller of
// ctx may list in the drafts queue and of a role item whose document the
// draft's own route shows that caller. It runs no check. It reads the
// newest 1000 open drafts that hold a role item (draftScanMax) and their
// items from the store, and compares each role's covers line in a draft
// with the live one the gateway holds, by the ids the list resolves. The
// caller's read standing is read only when a draft changes a covers line
// on app. A failed read is an error.
func (a *App) objectDrafts(ctx context.Context, app string, list *objects.List) (func(id string) []objectDraft, error) {
	rows, err := a.openRoleDrafts(ctx)
	if err != nil || len(rows) == 0 {
		return func(string) []objectDraft { return nil }, err
	}
	ids := make([]int64, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	items, err := a.store.Drafts().Items(ctx, ids)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string, len(list.Objects))
	for _, o := range list.Objects {
		values[o.Value] = o.ID
	}
	changes := coversChanges(app, a.liveCovers(app), values, rows, items)
	if len(changes) == 0 {
		return func(string) []objectDraft { return nil }, nil
	}
	author, err := a.directAuthor(ctx)
	if err != nil {
		return nil, err
	}
	c, err := a.proposerCaller(ctx, author)
	if err != nil {
		return nil, err
	}
	// The queue reads an admin API token as no person, so a token without
	// a drafts grant lists no draft there and reads none here.
	c.person = c.person && !c.adminAPI()
	listed := map[int64]bool{}
	if c.mayUse(http.MethodGet) {
		if listed, err = a.listable(ctx, c, rows, items); err != nil {
			return nil, err
		}
	}
	authorOnly := !c.p.root && !c.p.scope.Grants["drafts:read"]
	changes = slices.DeleteFunc(changes, func(ch coversChange) bool { return !listed[ch.draft] || authorOnly && !c.readsRoleItem(ch.item) })
	return func(id string) []objectDraft { return draftsOn(changes, id) }, nil
}

// readsRoleItem reports whether c may read the role item it of a stored
// draft, as itemPayloads judges it for a reader without root or
// drafts:read: readRefusal's role rule, with the owner itemServer reads
// from the item's stamp, so a role of no server needs identity:read.
func (c draftCaller) readsRoleItem(it store.DraftItemRow) bool {
	owner := itemServer(it)
	w := drafts.World{Roles: map[string]drafts.Role{it.Name: {Name: it.Name, Owner: owner, Owned: owner != ""}}}
	return c.readsObject(w, drafts.Item{Kind: drafts.KindRole, Name: it.Name, Op: drafts.Op(it.Op), Doc: it.Doc})
}

// openRoleDrafts answers the open drafts that hold a role item, newest
// first, at most draftScanMax of them, in store pages of draftListMax.
func (a *App) openRoleDrafts(ctx context.Context) ([]store.DraftRow, error) {
	f := store.DraftFilter{State: string(drafts.StateOpen), Object: store.ObjectRef{Kind: string(drafts.KindRole)}}
	var out []store.DraftRow
	var before int64
	for len(out) < draftScanMax {
		asked := min(draftListMax, draftScanMax-len(out))
		page, err := a.store.Drafts().List(ctx, f, before, asked)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if len(page) < asked {
			break
		}
		before = page[len(page)-1].ID
	}
	return out, nil
}

// liveCovers answers what each role with a binding on the server app
// covers there now, from the bindings the gateway holds. A binding with no
// covers line covers nothing.
func (a *App) liveCovers(app string) map[string]coverSet {
	bindings, _ := a.gateway.bindings.Load().([]gwBinding)
	out := map[string]coverSet{}
	for _, b := range bindings {
		if b.App != app {
			continue
		}
		s := out[b.Role]
		if b.Scope != nil {
			s.every = s.every || b.Scope.Every
			for _, o := range b.Scope.Objects {
				if s.ids == nil {
					s.ids = map[string]bool{}
				}
				s.ids[o.ID] = true
			}
		}
		out[b.Role] = s
	}
	return out
}

// coversChanges answers the role items of the drafts rows, whose items are
// items, that change what a role covers on the server app, sorted by draft
// and then role. live is what each role with a binding on app covers now,
// and values maps an object's value to its id. A removed role, and a role
// whose document binds no tool of app or has no covers line there, covers
// nothing after the draft. A value the list does not hold reaches no
// object, and a document that does not read is left to the draft's check.
func coversChanges(app string, live map[string]coverSet, values map[string]string, rows []store.DraftRow, items map[int64][]store.DraftItemRow) []coversChange {
	var out []coversChange
	for _, row := range rows {
		for _, it := range items[row.ID] {
			if it.Kind != string(drafts.KindRole) {
				continue
			}
			now, bound := live[it.Name]
			var next coverSet
			switch drafts.Op(it.Op) {
			case drafts.OpRemove:
			case drafts.OpPut:
				doc, err := drafts.ParseRole(it.Doc)
				if err != nil {
					continue
				}
				i := slices.IndexFunc(doc.Spec.Bindings, func(b drafts.RoleBinding) bool { return b.App == app })
				if i < 0 && !bound {
					continue
				}
				if i >= 0 {
					next = coversOf(doc.Spec.Bindings[i].Covers, values)
				}
			default:
				continue
			}
			if !now.equal(next) {
				out = append(out, coversChange{draft: row.ID, role: it.Name, live: now, next: next, item: it})
			}
		}
	}
	slices.SortFunc(out, func(x, y coversChange) int { return cmp.Or(cmp.Compare(x.draft, y.draft), cmp.Compare(x.role, y.role)) })
	return out
}

// coversOf is what the covers line s of a role document reaches, with
// each value resolved to its id by values.
func coversOf(s *drafts.Scope, values map[string]string) coverSet {
	if s == nil {
		return coverSet{}
	}
	if s.Every {
		return coverSet{every: true}
	}
	out := coverSet{ids: map[string]bool{}}
	for _, o := range s.Objects {
		if id, ok := values[o.Value]; ok {
			out.ids[id] = true
		}
	}
	return out
}

// draftsOn answers the changes that add or drop a role on the object with
// the id, in the order of changes, or nil when none does.
func draftsOn(changes []coversChange, id string) []objectDraft {
	var out []objectDraft
	for _, ch := range changes {
		now, next := ch.live.has(id), ch.next.has(id)
		switch {
		case next && !now:
			out = append(out, objectDraft{Draft: strconv.FormatInt(ch.draft, 10), Role: ch.role, Change: "adds"})
		case now && !next:
			out = append(out, objectDraft{Draft: strconv.FormatInt(ch.draft, 10), Role: ch.role, Change: "drops"})
		}
	}
	return out
}
