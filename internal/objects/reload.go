package objects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/strazahq/straza/internal/store"
)

// errMoved says a list changed in the store while it was read; the next
// tick reads it again.
var errMoved = errors.New("objects: the list changed while it was read")

// reload brings memory in line with the store: a list whose row went is
// dropped, every list takes the status of its row, and a list whose
// version or declaration differs from the one held loads its objects. A
// list that fails to load keeps what this replica held.
func (l *Lists) reload(ctx context.Context) error {
	l.loadMu.Lock()
	defer l.loadMu.Unlock()
	rows, err := l.opts.Store.ObjectLists().List(ctx)
	if err != nil {
		return err
	}
	next := make(map[string]*List, len(rows))
	for _, row := range rows {
		cur := l.Get(row.AppID)
		if cur != nil && cur.Version == row.Version && cur.DeclHash == row.DeclHash {
			next[row.AppID] = cur.withStatus(row)
			continue
		}
		lst, err := l.load(ctx, row)
		if err != nil {
			if !errors.Is(err, errMoved) {
				l.opts.Log.Warn("object lists: load a list failed", "app", row.AppID, "err", err)
			}
			if cur != nil {
				next[row.AppID] = cur
			}
			continue
		}
		next[row.AppID] = lst
	}
	l.mu.Lock()
	l.lists = next
	l.mu.Unlock()
	return nil
}

// loadOne loads the list of appID at once, as the replica that just
// listed it does.
func (l *Lists) loadOne(ctx context.Context, appID string) {
	l.loadMu.Lock()
	defer l.loadMu.Unlock()
	row, err := l.opts.Store.ObjectLists().Get(ctx, appID)
	if err == nil {
		var lst *List
		if lst, err = l.load(ctx, row); err == nil {
			l.mu.Lock()
			l.lists[appID] = lst
			l.mu.Unlock()
			return
		}
	}
	if !errors.Is(err, errMoved) {
		l.opts.Log.Warn("object lists: load a list failed", "app", appID, "err", err)
	}
}

// load reads the objects of row and answers its list. It answers errMoved
// when the row's version or declaration moved during the read.
func (l *Lists) load(ctx context.Context, row store.ObjectList) (*List, error) {
	ol := l.opts.Store.ObjectLists()
	rows, err := ol.Objects(ctx, row.AppID)
	if err != nil {
		return nil, err
	}
	after, err := ol.Get(ctx, row.AppID)
	if err != nil {
		return nil, err
	}
	if after.Version != row.Version || after.DeclHash != row.DeclHash {
		return nil, errMoved
	}
	objs := make([]Object, len(rows))
	for i, r := range rows {
		o := Object{Value: r.Value, ID: r.ID, Title: r.Title, AddedVersion: r.AddedVersion}
		if err := json.Unmarshal([]byte(r.Attributes), &o.Attributes); err != nil {
			return nil, fmt.Errorf("objects: the attributes of %s: %w", r.Value, err)
		}
		objs[i] = o
	}
	return newList(after, objs), nil
}
