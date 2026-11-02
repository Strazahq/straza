package server

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/objects"
	"github.com/strazahq/straza/internal/store"
)

// objectsReview reads the straza.objects block of mf, the manifest of an
// App item named name, against every tool its running server lists now,
// and answers nil when the manifest pins no object type. A server with no
// tool list on this replica gets a review that says it is not running.
func (a *App) objectsReview(name string, mf manager.Manifest) *drafts.ObjectsReview {
	o := mf.Straza.Objects
	if o == nil {
		return nil
	}
	r := &drafts.ObjectsReview{Type: o.Type, Plural: o.PluralWord()}
	if tools, err := a.manager.LiveInventory(name); err == nil {
		r.Running = true
		r.Refusals, r.Undecided, r.Unknown = manager.ReviewObjects(name, *o, tools)
	}
	return r
}

// pinnedFacts is the pinned object type of mf as a draft check reads it,
// nil when mf pins none: the type, its plural, the reach of each tool the
// block names, the argument of each argument tool and the listing tool.
// readObjectFacts adds the listing credential and the ids for the servers
// a draft names.
func pinnedFacts(mf manager.Manifest) *drafts.PinnedObjects {
	o := mf.Straza.Objects
	if o == nil {
		return nil
	}
	p := &drafts.PinnedObjects{Type: o.Type, Plural: o.PluralWord(), ListTool: o.List.Tool, Reach: make(map[string]string, len(o.Tools)), Argument: map[string]string{}}
	for tool, r := range o.Tools {
		p.Reach[tool] = r.Reach
		if r.Argument != "" {
			p.Argument[tool] = r.Argument
		}
	}
	return p
}

// readObjectFacts fills the pinned object facts of every server d names as
// an App put or as the server of a Role put's access row, live in w and
// after the draft in apps: the listing credential's state, the id of each
// value the Role puts name that the server's object list holds, and, for
// an App put, the type the live manifest pins. It reads the object list
// once per server and never a credential's value.
func (a *App) readObjectFacts(ctx context.Context, w drafts.World, d drafts.Draft, apps map[string]drafts.App) error {
	named, values := map[string]bool{}, map[string][]string{}
	for _, it := range d.Items {
		switch {
		case it.Kind == drafts.KindApp && it.Op == drafts.OpPut:
			named[it.Name] = true
		case it.Kind == drafts.KindRole && it.Op == drafts.OpPut:
			doc, err := drafts.ParseRole(it.Doc)
			if err != nil {
				continue
			}
			for _, b := range doc.Spec.Bindings {
				named[b.App] = true
				values[b.App] = append(values[b.App], coversValues(b.Covers)...)
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(named)) {
		live, isLive := w.Apps[name]
		next := apps[name]
		if live.Pinned == nil && next.Pinned == nil {
			continue
		}
		liveList, err := a.listFor(ctx, live.ID, live.Manifest)
		if err != nil {
			return fmt.Errorf("the object list of %s cannot be read: %w", name, err)
		}
		nextList, err := a.listFor(ctx, live.ID, next.Manifest)
		if err != nil {
			return fmt.Errorf("the object list of %s cannot be read: %w", name, err)
		}
		var ids map[string]string
		if isLive && (liveList || nextList) && len(values[name]) > 0 {
			if ids, err = a.store.ObjectLists().ObjectsByValues(ctx, live.ID, values[name]); err != nil {
				return fmt.Errorf("the object list of %s cannot be read: %w", name, err)
			}
		}
		if p := live.Pinned; p != nil {
			p.Credential = a.listingState(live.ID, live.Manifest)
			if liveList {
				p.IDs = ids
			}
		}
		if p := next.Pinned; p != nil {
			p.Credential = a.listingState(live.ID, next.Manifest)
			if nextList {
				p.IDs = ids
			}
			if live.Pinned != nil {
				p.LiveType = live.Pinned.Type
			}
		}
	}
	return nil
}

// readCurrentValues gives every named object of w.Access the value its
// server's object list holds now (currentValues), so a role's document
// names a renamed object as the export does, and a draft's value meets
// the live row under the name the list gives it today.
func (a *App) readCurrentValues(ctx context.Context, w *drafts.World) error {
	ids := map[string][]string{}
	for _, acc := range w.Access {
		ids[acc.Server] = append(ids[acc.Server], coversIDs(acc.Covers)...)
	}
	for server, list := range ids {
		app, ok := w.Apps[server]
		if !ok || len(list) == 0 {
			continue
		}
		values, err := a.currentValues(ctx, app.ID, app.Manifest, list)
		if err != nil {
			return fmt.Errorf("the object list of %s cannot be read: %w", server, err)
		}
		for role, acc := range w.Access {
			if acc.Server == server && acc.Covers != nil {
				acc.Covers = withCurrentValues(acc.Covers, values)
				w.Access[role] = acc
			}
		}
	}
	return nil
}

// listFor reports whether the stored object list of the server with the id
// appID is the listing of the objects block the manifest JSON manifest
// pins: the same type and the same declaration. A list the listing loop
// has not started over since a type change, an unpin or another
// declaration holds no object of the block pinned now, so its ids and
// values are never read for it. A server the draft adds has no list.
func (a *App) listFor(ctx context.Context, appID, manifest string) (bool, error) {
	if appID == "" || manifest == "" {
		return false, nil
	}
	mf, err := manager.FromJSON(manifest)
	if err != nil {
		return false, nil
	}
	spec := pinnedObjects(mf)
	if spec == nil {
		return false, nil
	}
	row, err := a.store.ObjectLists().Get(ctx, appID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return row.Type == spec.Type && row.DeclHash == objects.DeclHash(*spec), nil
}

// currentValues answers the value the object list of the server with the
// id appID holds now for each of ids, by id, when that list is the listing
// of the block manifest pins, and nothing otherwise. An id the list no
// longer holds is absent, so a caller keeps the value stored with it.
func (a *App) currentValues(ctx context.Context, appID, manifest string, ids []string) (map[string]string, error) {
	ok, err := a.listFor(ctx, appID, manifest)
	if err != nil || !ok || len(ids) == 0 {
		return nil, err
	}
	return a.store.ObjectLists().ObjectsByIDs(ctx, appID, ids)
}

// withCurrentValues answers a copy of s with each object's value as values
// holds it, by id, the stored value where values lacks the id, sorted by
// value. nil answers nil.
func withCurrentValues(s *drafts.Scope, values map[string]string) *drafts.Scope {
	if s == nil {
		return nil
	}
	out := &drafts.Scope{Every: s.Every, Objects: slices.Clone(s.Objects)}
	for i, o := range out.Objects {
		if v, ok := values[o.ID]; ok {
			out.Objects[i].Value = v
		}
	}
	slices.SortStableFunc(out.Objects, func(x, y drafts.ObjectRef) int { return strings.Compare(x.Value, y.Value) })
	return out
}

// listingState is the listing credential state of the server with the id
// appID, empty for a server the draft adds, under the manifest JSON
// manifest: set, missing or not-needed. A manifest that does not decode
// reads as missing, the state that informs.
func (a *App) listingState(appID, manifest string) string {
	mf, err := manager.FromJSON(manifest)
	if err != nil {
		return "missing"
	}
	return a.listingCredential(appID, mf)
}

// coversValues answers the values a covers line names, nil for none.
func coversValues(s *drafts.Scope) []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s.Objects))
	for i, o := range s.Objects {
		out[i] = o.Value
	}
	return out
}

// coversIDs answers the ids a covers line names, nil for none.
func coversIDs(s *drafts.Scope) []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s.Objects))
	for i, o := range s.Objects {
		out[i] = o.ID
	}
	return out
}

// draftScope is the stored covers line s as a drafts.World holds it, nil
// for nil.
func draftScope(s *store.ObjectScope) *drafts.Scope {
	if s == nil {
		return nil
	}
	out := &drafts.Scope{Every: s.Every}
	for _, o := range s.Objects {
		out.Objects = append(out.Objects, drafts.ObjectRef{Value: o.Value, ID: o.ID})
	}
	return out
}

// storeScope is the covers line s in the store's words, nil for nil.
func storeScope(s *drafts.Scope) *store.ObjectScope {
	if s == nil {
		return nil
	}
	out := &store.ObjectScope{Every: s.Every}
	for _, o := range s.Objects {
		out.Objects = append(out.Objects, store.CoveredObject{ID: o.ID, Value: o.Value})
	}
	return out
}
