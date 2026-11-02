package objects

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// Load reads every object list into memory, as a replica does at boot
// before it serves.
func (l *Lists) Load(ctx context.Context) error { return l.reload(ctx) }

// Run keeps the object lists current until ctx ends: every tick, and when
// a refresh or a miss wakes it, it brings the rows in line with the pinned
// declarations, lists the servers that are due and that this replica
// claims, and loads every newer version into memory. It waits for the
// listings it started before it returns.
func (l *Lists) Run(ctx context.Context) {
	t := time.NewTicker(l.every)
	defer t.Stop()
	defer l.wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-l.wake:
		}
		l.tick(ctx)
	}
}

// tick is one pass of the loop. The rows are read before the apps, so the
// declarations this replica sees are never older than the hash a row
// carries, and a replica never starts a list over with a stale one. While
// the lists are off, it does nothing.
func (l *Lists) tick(ctx context.Context) {
	if l.opts.Off {
		return
	}
	ol := l.opts.Store.ObjectLists()
	rows, err := ol.List(ctx)
	if err != nil {
		l.opts.Log.Warn("object lists: read the lists failed", "err", err)
		return
	}
	targets, err := l.targets(ctx)
	if err != nil {
		l.opts.Log.Warn("object lists: read the servers failed", "err", err)
		return
	}
	now := l.now()
	l.align(ctx, rows, targets, now)
	l.takeMisses(ctx, now)
	if rows, err = ol.List(ctx); err != nil {
		l.opts.Log.Warn("object lists: read the lists failed", "err", err)
		return
	}
	for _, row := range rows {
		if t, ok := targets[row.AppID]; ok && t.declHash == row.DeclHash && claimable(row, now) {
			l.start(ctx, t)
		}
	}
	if err := l.reload(ctx); err != nil {
		l.opts.Log.Warn("object lists: reload failed", "err", err)
	}
}

// targets reads every server whose stored manifest pins an object type.
func (l *Lists) targets(ctx context.Context) (map[string]target, error) {
	apps, err := l.opts.Store.Apps().List(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]target{}
	for _, app := range apps {
		mf, err := manager.FromJSON(app.Manifest)
		if err != nil || mf.Straza.Objects == nil || mf.Straza.Objects.Type == "" {
			continue
		}
		spec := *mf.Straza.Objects
		out[app.ID] = target{app: app, spec: spec, declHash: DeclHash(spec), needsSecret: manager.ListingNeedsSecret(mf)}
	}
	return out, nil
}

// align makes one row per target, starts the row of a changed declaration
// over, and deletes the rows of servers that pin no type any more.
func (l *Lists) align(ctx context.Context, rows []store.ObjectList, targets map[string]target, now time.Time) {
	ol := l.opts.Store.ObjectLists()
	have := map[string]bool{}
	for _, row := range rows {
		have[row.AppID] = true
		t, ok := targets[row.AppID]
		var err error
		switch {
		case !ok:
			err = ol.Delete(ctx, row.AppID)
		case t.declHash != row.DeclHash:
			if err = ol.Reset(ctx, row.AppID, t.spec.Type, t.declHash, now); errors.Is(err, store.ErrNotFound) {
				err = nil // another replica started it over first
			}
		}
		if err != nil {
			l.opts.Log.Warn("object lists: align a list failed", "app", row.AppID, "err", err)
		}
	}
	for id, t := range targets {
		if have[id] {
			continue
		}
		if err := ol.Ensure(ctx, id, t.spec.Type, t.declHash, now); err != nil {
			l.opts.Log.Warn("object lists: create a list failed", "app", t.app.Name, "err", err)
		}
	}
}

// takeMisses marks due every server a call missed on since the last tick,
// unless it was listed within the last minute.
func (l *Lists) takeMisses(ctx context.Context, now time.Time) {
	l.missMu.Lock()
	missed := l.missed
	l.missed = map[string]bool{}
	l.missMu.Unlock()
	for id := range missed {
		if _, err := l.opts.Store.ObjectLists().MarkDueIfIdle(ctx, id, now, now.Add(-time.Minute)); err != nil && !errors.Is(err, store.ErrNotFound) {
			l.opts.Log.Warn("object lists: mark a missed list due failed", "app", id, "err", err)
		}
	}
}

// claimable reports whether row is due at now and no live claim holds it.
func claimable(row store.ObjectList, now time.Time) bool {
	return !row.DueAt.After(now) && (row.ClaimOwner == "" || row.ClaimUntil == nil || now.After(*row.ClaimUntil))
}

// start claims the listing of t and runs it in the background when this
// replica gets the claim and is not listing t already.
func (l *Lists) start(ctx context.Context, t target) {
	l.missMu.Lock()
	busy := l.inflight[t.app.ID]
	l.missMu.Unlock()
	if busy {
		return
	}
	now := l.now()
	row, ok, err := l.opts.Store.ObjectLists().Claim(ctx, t.app.ID, l.opts.Owner, now, now.Add(l.claimFor))
	if err != nil || !ok {
		if err != nil {
			l.opts.Log.Warn("object lists: claim a list failed", "app", t.app.Name, "err", err)
		}
		return
	}
	l.missMu.Lock()
	l.inflight[t.app.ID] = true
	l.missMu.Unlock()
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		defer func() {
			l.missMu.Lock()
			delete(l.inflight, t.app.ID)
			l.missMu.Unlock()
		}()
		l.list(ctx, t, row)
	}()
}

// list runs one claimed listing and settles it: a changed result replaces
// the objects under a new version, an unchanged one keeps them, and a
// failure keeps them with its reason. A failure of a server that should be
// running but is not running on this replica yet is tried again after a
// minute, any other after the interval. A listing that renames objects
// calls OnRenamed once with their ids. A listing cut short by shutdown
// settles nothing, and its claim runs out.
func (l *Lists) list(ctx context.Context, t target, before store.ObjectList) {
	objs, reason, soon := l.fetch(ctx, t)
	if ctx.Err() != nil {
		return
	}
	at := l.now()
	ol := l.opts.Store.ObjectLists()
	var row store.ObjectList
	var err error
	changed := false
	switch {
	case reason != "":
		due := at.Add(l.interval)
		if soon {
			due = at.Add(time.Minute)
		}
		row, err = ol.Settle(ctx, t.app.ID, l.opts.Owner, t.declHash,
			store.Listing{Outcome: store.ObjectsFailed, Reason: reason, At: at, Due: due})
	default:
		sort.Slice(objs, func(i, j int) bool { return objs[i].Value < objs[j].Value })
		hash := listHash(objs)
		if changed = hash != before.ListHash; changed {
			row, err = ol.Replace(ctx, t.app.ID, l.opts.Owner, t.declHash, hash, stored(objs),
				store.Listing{Outcome: store.ObjectsOK, At: at, Due: at.Add(l.interval)})
		} else {
			row, err = ol.Settle(ctx, t.app.ID, l.opts.Owner, t.declHash,
				store.Listing{Outcome: store.ObjectsOK, At: at, Due: at.Add(l.interval)})
		}
	}
	if err != nil {
		l.opts.Log.Warn("object lists: store a listing failed", "app", t.app.Name, "err", err)
		return
	}
	if changed || row.Outcome != before.Outcome {
		l.record(ctx, t, row, changed)
	}
	l.loadOne(ctx, t.app.ID)
	if len(row.Renamed) > 0 && l.opts.OnRenamed != nil {
		l.opts.OnRenamed(context.WithoutCancel(ctx), t.app.ID, row.Renamed)
	}
}

// record writes the actorless apps.objects.list record of one listing that
// changed the list or flipped its outcome. joined and left are the counts
// of this listing's change, zero when the list stayed as it was.
func (l *Lists) record(ctx context.Context, t target, row store.ObjectList, changed bool) {
	if l.opts.Record == nil {
		return
	}
	data := map[string]any{
		"action": "apps.objects.list", "target": t.app.ID, "app": t.app.Name,
		"tool": t.spec.List.Tool, "type": t.spec.Type, "outcome": row.Outcome,
		"count": row.Count, "version": row.Version,
	}
	if row.Outcome == store.ObjectsOK {
		data["joined"], data["left"] = 0, 0
		if changed {
			data["joined"], data["left"] = row.Joined, row.Left
		}
	} else {
		data["reason"] = row.Reason
	}
	l.opts.Record(context.WithoutCancel(ctx), data)
}

// stored is objs as the store keeps them.
func stored(objs []Object) []store.ListedObject {
	out := make([]store.ListedObject, len(objs))
	for i, o := range objs {
		attrs, _ := json.Marshal(o.Attributes)
		out[i] = store.ListedObject{ID: o.ID, Value: o.Value, Title: o.Title, Attributes: string(attrs)}
	}
	return out
}
