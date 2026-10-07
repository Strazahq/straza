package server

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/tokenscopes"
)

// draftsCheckInterval is how often the off-path checker runs on every
// replica.
const draftsCheckInterval = 30 * time.Second

// The pages of one checker tick: the unchecked drafts it checks and the
// expired drafts it closes. A tick that fills a page leaves the rest
// to the next tick.
const (
	uncheckedPage = 20
	expirablePage = 50
)

// runDraftsChecker runs checkDrafts every 30 seconds, and at once when a
// submit wakes it, until ctx ends, off every request path, and logs a
// failed tick, which the next tick repeats.
func (a *App) runDraftsChecker(ctx context.Context) {
	t := time.NewTicker(draftsCheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-a.draftsWake:
		}
		if err := a.checkDrafts(ctx); err != nil && ctx.Err() == nil {
			a.log.Warn("drafts checker: a step of the tick failed, and it runs again within 30 seconds", "err", err)
		}
	}
}

// wakeDraftsChecker asks the checker for a tick now. A wake already
// pending covers this one, so it never blocks.
func (a *App) wakeDraftsChecker() {
	select {
	case a.draftsWake <- struct{}{}:
	default:
	}
}

// checkDrafts is one tick of the checker: the policy conversion again,
// which settles a saved edit an older replica stored during a
// rolling upgrade, then the expiry of the straza-app drafts past their
// expiry, then the check of the drafts whose current revision no one
// checked. Each step runs whatever the one before answered, and the tick
// answers every step's error. The conversion takes a.configMu first, and
// the two sweeps run without it, as the drafts routes' checks do.
func (a *App) checkDrafts(ctx context.Context) error {
	var errs []error
	if err := a.convertPolicies(ctx); err != nil {
		errs = append(errs, fmt.Errorf("the policy conversion: %w", err))
	}
	if err := a.expireDrafts(ctx); err != nil {
		errs = append(errs, fmt.Errorf("the draft expiry: %w", err))
	}
	if err := a.checkUnchecked(ctx); err != nil {
		errs = append(errs, fmt.Errorf("the check of unchecked drafts: %w", err))
	}
	return errors.Join(errs...)
}

// expireDrafts closes as expired every open draft whose expiry passed, at
// the revision the list read, so a draft revised since keeps its new
// expiry. The replica whose close lands writes the one draft.expire, with
// no actor, because the expiry is nobody's act.
func (a *App) expireDrafts(ctx context.Context) error {
	now := time.Now()
	rows, err := a.store.Drafts().ListExpirable(ctx, now, expirablePage)
	if err != nil {
		return err
	}
	for _, row := range rows {
		closed, err := a.store.Drafts().Close(ctx, row.ID, row.Revision, "expired", store.DraftActor{}, "", now)
		if err != nil {
			return fmt.Errorf("close draft %d: %w", row.ID, err)
		}
		if closed {
			a.emitEventCtx(noActor(ctx), "straza.audit.admin", map[string]any{
				"action": "draft.expire", "draft": strconv.FormatInt(row.ID, 10), "revision": row.Revision})
		}
	}
	return nil
}

// checkUnchecked checks every open draft whose current revision no one
// checked: the drafts of the straza-app door, and the saved edits PUT and
// the conversion store stamped but unchecked. Each reads live state
// through draftWorld, as the drafts routes do, and is stamped with the
// verdict an agent reads when stampOf keeps one. The replica whose stamp
// lands writes the one draft.check. A draft whose check fails is
// left for the next tick, and the others still run.
func (a *App) checkUnchecked(ctx context.Context) error {
	rows, err := a.store.Drafts().ListUnchecked(ctx, uncheckedPage)
	if err != nil || len(rows) == 0 {
		return err
	}
	ids := make([]int64, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	items, err := a.store.Drafts().Items(ctx, ids)
	if err != nil {
		return err
	}
	var errs []error
	for _, row := range rows {
		if err := a.checkOne(ctx, row, items[row.ID]); err != nil {
			errs = append(errs, fmt.Errorf("draft %d: %w", row.ID, err))
		}
	}
	return errors.Join(errs...)
}

// checkOne checks the draft row, whose current items are items, and
// stamps the check when no other check of the revision landed first. A
// draft of the straza-app door meets intake again here, because its submit
// stored it with live state unread: the findings live state holds already
// are waived into the verdict's warnings (drafts.Waive) under the read
// standing of the author of its latest revision, as the drafts routes
// waive for their caller, and the rest join its refusals once each, as
// waiveFor answers them, so straza__draft_status names them.
func (a *App) checkOne(ctx context.Context, row store.DraftRow, items []store.DraftItemRow) error {
	revs, err := a.store.Drafts().Revisions(ctx, row.ID)
	if err != nil || len(revs) == 0 {
		return err
	}
	d := draftOf(row, items, authorsOf(revs))
	world, in, err := a.draftWorld(ctx, d)
	if err != nil {
		return err
	}
	author := principalOf(revs[len(revs)-1].Author)
	c, err := a.proposerCaller(ctx, author)
	if err != nil {
		return err
	}
	var fs []drafts.Finding
	if d.Door == drafts.DoorAgent {
		fs = intakeOf(d, author)
	}
	cd, err := a.checkRows(ctx, world, in, d, items)
	if err != nil {
		return err
	}
	cd = waiveFor(c, cd, fs)
	landed, err := a.store.Drafts().Stamp(ctx, row.ID, row.Revision, cd.rows, stampOf(cd.d, cd.v, cd.in.Now))
	if err != nil || !landed {
		return err
	}
	a.recordCheck(ctx, d.ID, cd.v)
	return nil
}

// proposerCaller answers the read standing of the principal p as
// requireDrafts builds it for a request of theirs: for a user, the roles
// the resolver answers now, root for the admin role, the grants
// admin.roleAreas maps, and the servers whose admin role they hold; for an
// admin API token, its stored scope, read by id. A revoked token and the
// upgrade's actor, which is no user, read nothing. The checker and the
// direct routes waive under it, as the drafts routes waive for their
// caller.
func (a *App) proposerCaller(ctx context.Context, p drafts.Principal) (draftCaller, error) {
	c := draftCaller{author: p, door: drafts.DoorAgent, person: !p.Agent}
	if p.UserID == "" {
		return c, nil
	}
	if p.Via == laneAdminAPI {
		tokens, err := a.apiTokens(ctx)
		if err != nil {
			return draftCaller{}, fmt.Errorf("the admin API tokens cannot be read: %w", err)
		}
		for _, m := range tokens {
			if m.ID != p.UserID {
				continue
			}
			scope, err := tokenscopes.Parse(m.Scope)
			if err != nil {
				return draftCaller{}, fmt.Errorf("the scope of the admin API token %s cannot be read: %w", p.Username, err)
			}
			c.p = adminPrincipal{actor: auditActor{Name: p.Username, ID: p.UserID, Via: p.Via}, scope: scope, root: scope.Full}
		}
		return c, nil
	}
	roles, err := a.resolver.ResolveRoles(ctx, p.UserID, time.Now())
	if err != nil {
		return draftCaller{}, fmt.Errorf("the roles of %s cannot be resolved: %w", p.Username, err)
	}
	if roles == nil {
		roles = []store.Role{}
	}
	c.p = adminPrincipal{actor: auditActor{Name: p.Username, ID: p.UserID, Via: p.Via}, roles: roles, scope: a.scopeForRoles(roles)}
	ids := make([]string, len(roles))
	for i, role := range roles {
		ids[i] = role.ID
		c.p.root = c.p.root || role.Name == AdminRole
	}
	if c.p.root {
		return c, nil
	}
	rows, err := a.store.Apps().ListByAdminRoles(ctx, ids)
	if err != nil {
		return draftCaller{}, fmt.Errorf("the servers %s administers cannot be read: %w", p.Username, err)
	}
	c.servers = make(map[string]string, len(rows))
	for _, row := range rows {
		c.servers[row.ID] = row.Name
	}
	return c, nil
}
