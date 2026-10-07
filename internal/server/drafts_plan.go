package server

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// draftPlan is a checked draft ready to commit: the store's plan, the head
// the apply runs first on this replica, the servers the publish writes, and
// what its records read.
type draftPlan struct {
	plan    store.PublishPlan
	head    publishHead
	servers []serverChange
	records recordContext
}

// serverChange is a server a publish writes, with its change in the words
// of the publish answer: created, changed or removed.
type serverChange struct{ name, change string }

// The changes of a server in the publish answer.
const (
	serverCreated = "created"
	serverChanged = "changed"
	serverRemoved = "removed"
)

// planFor turns draft d, stamped in rows and checked over live state w with
// the item facts of in, into the plan that publishes it: its items in draft
// order, then the objects its removals take with them (drafts.Implied).
// Each item's After is what the store reads back once it wrote the item: a
// role's is read over the World after the draft, so a role that loses an
// edge or a row to a removal in the same draft reads as the store leaves
// it. The plan carries the World's generation and snapshot. The caller
// sets DraftID and Revision or New, Publisher, Acks, Close and the
// draft.publish fields, which differ between the drafts route and a direct
// route. An App document that no longer parses, which Check refuses, is an
// error.
func planFor(w drafts.World, in drafts.CheckInput, d drafts.Draft, rows []store.DraftItemRow) (draftPlan, error) {
	after := w.Overlay(d, in.Apps)
	dp := draftPlan{plan: store.PublishPlan{Generation: w.Generation, BaseSnapshot: w.SnapshotID},
		head:    publishHead{Before: w.Implies, After: after.Implies},
		records: recordContext{w: w, changed: map[string][]string{}, changes: map[string][]byte{}}}
	for _, row := range rows {
		pi := store.PlanItem{Ref: store.ObjectRef{Kind: row.Kind, Name: row.Name}, Op: row.Op, Base: row.Base, BaseOp: row.BaseOp, BaseDoc: row.BaseDoc}
		if err := dp.explicit(&pi, after, d.Door, row.Doc); err != nil {
			return draftPlan{}, err
		}
		dp.plan.Items = append(dp.plan.Items, pi)
	}
	for _, it := range drafts.Implied(w, d) {
		pi := store.PlanItem{Ref: store.ObjectRef{Kind: string(it.Kind), Name: it.Name}, Op: string(it.Op), Implied: true,
			Base: string(w.Fingerprints[it.Object()]), BaseOp: string(drafts.OpPut)}
		if doc, ok := drafts.RoleDocOf(w, it.Name); ok {
			text, err := doc.Marshal()
			if err != nil {
				return draftPlan{}, fmt.Errorf("the role %s cannot be written as a document: %w", it.Name, err)
			}
			pi.BaseDoc = string(text)
		}
		if it.Op == drafts.OpPut {
			pi.After, pi.AfterDoc = store.FingerprintRole(roleConfig(after, it.Name)), it.Doc
		}
		dp.plan.Items = append(dp.plan.Items, pi)
	}
	dp.name(w, after)
	dp.records.items = dp.plan.Items
	return dp, nil
}

// explicit fills the plan item pi of a draft item whose document is doc:
// what the store writes for its kind and op, and its After. A server's
// document is read with every mask it sends back replaced by the value
// the live manifest holds there (keptMasks), because the check waived the
// mask for that value and a stored manifest never holds the mark. A mask
// the check did not waive is an error, so a publish whose check missed it
// fails closed.
func (dp *draftPlan) explicit(pi *store.PlanItem, after drafts.World, door drafts.Door, doc string) error {
	name := pi.Ref.Name
	switch {
	case pi.Op == string(drafts.OpRemove):
		if _, on := dp.records.w.Policies[name]; on && pi.Ref.Kind == string(drafts.KindPolicySet) {
			dp.records.changes[name] = nil
		}
	case pi.Ref.Kind == string(drafts.KindApp):
		restored, at := keptMasks(doc, dp.records.w.Apps[name].Manifest)
		if restored == "" {
			return fmt.Errorf("the manifest of %s holds a mask at %s that the check did not waive, so it cannot be published", name, at)
		}
		mf, err := manager.Parse([]byte(restored))
		if err != nil {
			return fmt.Errorf("the manifest of %s does not parse: %w", name, err)
		}
		manifest, err := mf.JSON()
		if err != nil {
			return fmt.Errorf("the manifest of %s cannot be written as JSON: %w", name, err)
		}
		source := store.AppSourceAPI
		if door == drafts.DoorAppsDir {
			source = store.AppSourceGitops
		}
		pi.App = &store.App{Name: name, Version: mf.ServerVersion(), Manifest: manifest, RuntimeKind: mf.Straza.Runtime.Kind, Source: source}
		if pi.After, err = store.FingerprintApp(manifest); err != nil {
			return err
		}
		pi.AfterDoc = manifest
		if live, ok := dp.records.w.Apps[name]; ok && pi.After != pi.Base {
			if paths, err := manager.ChangedPaths([]byte(live.Manifest), []byte(manifest)); err == nil {
				dp.records.changed[name] = paths
			}
		}
	case pi.Ref.Kind == string(drafts.KindRole):
		cfg := roleConfig(after, name)
		cfg.Role.ID = ""
		pi.Role, pi.After, pi.AfterDoc = &cfg, store.FingerprintRole(cfg), doc
	default:
		set, err := policy.Parse([]byte(doc))
		if err != nil {
			return fmt.Errorf("the policy set %s does not parse: %w", name, err)
		}
		ps := store.PolicySet{Name: name, Priority: set.Spec.Priority, YAMLSource: doc, Status: "draft"}
		live, on := dp.records.w.Policies[name]
		switch {
		case pi.Op == string(drafts.OpPut):
			ps.Status = "active"
			if !on || live.Text != doc {
				dp.records.changes[name] = []byte(doc)
			}
		case on:
			dp.records.changes[name] = nil
		}
		pi.Policy, pi.After, pi.AfterDoc = &ps, store.FingerprintPolicySet(ps), doc
	}
	return nil
}

// name fills the head and the servers from the plan's items: an item
// writes something exactly when its After differs from its Base. A server
// put of a live server and a server removal stop its instances, a new
// server is announced once it runs, an explicit role put whose access row
// changes or goes narrows its rows, and every removed role, a removed
// server's admin role included, goes.
func (dp *draftPlan) name(w, after drafts.World) {
	for _, it := range dp.plan.Items {
		if it.After == it.Base {
			continue
		}
		name, remove := it.Ref.Name, it.Op == string(drafts.OpRemove)
		switch drafts.Kind(it.Ref.Kind) {
		case drafts.KindApp:
			change := serverChanged
			switch {
			case remove:
				change = serverRemoved
				if admin := w.Apps[name].AdminRole; admin != "" {
					dp.head.Removed = append(dp.head.Removed, admin)
				}
			case it.BaseOp != string(drafts.OpPut):
				change = serverCreated
			}
			if change != serverCreated {
				dp.head.Servers = append(dp.head.Servers, name)
			}
			dp.servers = append(dp.servers, serverChange{name, change})
		case drafts.KindRole:
			live, had := w.Access[name]
			next, has := after.Access[name]
			switch {
			case remove:
				dp.head.Removed = append(dp.head.Removed, name)
			case !it.Implied && (had != has || had && (live.Server != next.Server ||
				!slices.Equal(slices.Sorted(slices.Values(live.Tools)), slices.Sorted(slices.Values(next.Tools))))):
				dp.head.AccessRoles = append(dp.head.AccessRoles, name)
			}
		}
	}
}

// announce names the servers whose start the publish answers with: the
// ones it created or changed.
func (dp *draftPlan) announce() map[string]bool {
	out := map[string]bool{}
	for _, s := range dp.servers {
		if s.change != serverRemoved {
			out[s.name] = true
		}
	}
	return out
}

// commitEnd is how a commit ended.
type commitEnd int

// The ends of a commit. commitMoved and commitBusy send the publish back to
// its World read. commitDraftMoved means the draft is no longer open
// at the plan's revision. commitBuildFailed is a snapshot build refused on
// an unmoved base. commitUnconfirmed is any other Publish error, after
// which the transaction may have committed or not.
const (
	commitLanded commitEnd = iota
	commitMoved
	commitBusy
	commitDraftMoved
	commitBuildFailed
	commitUnconfirmed
)

// commit builds the snapshot of the plan's set changes on the snapshot the
// World w read, when a set's published text or presence moves, sets the
// plan's and the head's snapshot and the plan's records, and runs Publish.
// It answers the result, how it ended and the error behind every end but
// commitLanded. A Build that fails after another publish activated a new
// snapshot ends as commitMoved, so the publish runs again on it. The
// caller holds a.configMu and passes a context no client can cancel.
func (a *App) commit(ctx context.Context, w drafts.World, dp *draftPlan) (store.PublishResult, commitEnd, error) {
	if changes := dp.records.changes; len(changes) > 0 {
		built, err := a.snapshots.Build(ctx, w.SnapshotID, changes)
		if err != nil {
			if a.snapshotMoved(ctx, w.SnapshotID) {
				return store.PublishResult{}, commitMoved, err
			}
			return store.PublishResult{}, commitBuildFailed, err
		}
		dp.plan.Snapshot = &store.Snapshot{ID: built.ID, SignerKeyID: built.SignerKeyID, Size: int64(len(built.Blob)), Blob: built.Blob}
		dp.head.Snapshot = &built
		dp.records.built, dp.records.sets = true, built.Sets
	}
	dp.plan.Records = a.recordsFor(ctx, dp.records)
	res, err := a.store.Drafts().Publish(ctx, dp.plan)
	var conflict store.PublishConflict
	switch {
	case err == nil:
		return res, commitLanded, nil
	case !errors.As(err, &conflict):
		return res, commitUnconfirmed, err
	case conflict.Busy:
		return res, commitBusy, err
	case conflict.Draft:
		return res, commitDraftMoved, err
	}
	return res, commitMoved, err
}

// snapshotMoved reports whether the active snapshot is no longer base. A
// read that fails counts as moved, so the publish runs again and its World
// read answers the failure.
func (a *App) snapshotMoved(ctx context.Context, base string) bool {
	act, err := a.store.Snapshots().GetActive(ctx)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return base != ""
	case err != nil:
		return true
	}
	return act.ID != base
}
