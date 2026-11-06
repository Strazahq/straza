package objects

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// target is one server whose manifest pins an object type: its row, the
// pinned block, its hash, and whether its listing carries a credential.
type target struct {
	app         store.App
	spec        manager.ObjectsSpec
	declHash    string
	needsSecret bool
}

// fetch lists the objects of t page by page and answers them, or the
// reason the listing failed and whether that failure is one a server that
// is still starting answers: a server that should be running is not
// running on this replica yet, or Straza has not read its tool list yet. A
// server that takes a listing credential is called with exactly that one,
// and not at all without it. The listing tool must be on the server's live
// tool list and marked read-only.
func (l *Lists) fetch(ctx context.Context, t target) ([]Object, string, bool) {
	tool, plural := t.spec.List.Tool, t.spec.PluralWord()
	// A stop and a pause both leave the row stopped, so only such a server
	// is not running on purpose.
	starting := t.app.Status != manager.StatusStopped
	var secret *manager.Secret
	if t.needsSecret {
		if secret = l.opts.Secrets.ListingSecret(t.app.ID); secret == nil {
			return nil, reasonNoCredential(t.app.Name, plural), false
		}
	}
	tools, err := l.opts.Caller.LiveInventory(t.app.Name)
	switch {
	case errors.Is(err, manager.ErrToolsUnread):
		return nil, reasonToolsUnread(t.app.Name, tool, plural), starting
	case err != nil:
		return nil, reasonNotRunning(t.app.Name, tool), starting
	}
	found := false
	for _, tl := range tools {
		if tl.Name != tool {
			continue
		}
		found = true
		if tl.Annotations == nil || !tl.Annotations.ReadOnlyHint {
			return nil, reasonNotReadOnly(tool, plural), false
		}
	}
	if !found {
		return nil, reasonNoTool(t.app.Name, tool, plural), false
	}

	ctx, cancel := context.WithTimeout(ctx, l.listTimeout)
	defer cancel()
	c := newCollector(t.spec)
	cursor := ""
	var refused *manager.RefusedError
	for page := 1; ; page++ {
		args := json.RawMessage(`{}`)
		if cursor != "" {
			args, _ = json.Marshal(map[string]string{"cursor": cursor})
		}
		pageCtx, pageCancel := context.WithTimeout(ctx, l.pageTimeout)
		res, err := l.opts.Caller.CallListing(pageCtx, t.app.Name, tool, args, secret)
		pageErr := pageCtx.Err()
		pageCancel()
		switch {
		case err != nil && ctx.Err() != nil:
			return nil, reasonListingTimeout, false
		case err != nil && pageErr != nil:
			return nil, reasonPageTimeout(tool, page), false
		case errors.Is(err, manager.ErrNotReady):
			return nil, reasonNotRunning(t.app.Name, tool), starting
		case errors.As(err, &refused):
			return nil, reasonRefused(t.app.Name, tool, plural, refused.Status, secret != nil), false
		case err != nil || res == nil:
			return nil, reasonUnreachable(t.app.Name, tool), false
		case res.IsError:
			return nil, reasonToolError(t.app.Name, tool, res), false
		}
		next, reason := c.page(res)
		switch {
		case reason != "":
			return nil, reason, false
		case next == "":
			return c.objs, "", false
		case next == cursor:
			return nil, reasonCursorLoop(tool), false
		}
		cursor = next
	}
}
