package server

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// savedEditNote is the note of a saved edit the conversion found in a row.
const savedEditNote = "Saved before the upgrade to drafts: this edit was stored and never published."

// upgradeActor is strazad's own account, which proposes and writes the
// drafts of the conversion.
var upgradeActor = store.DraftActor{Name: "strazad", Via: "upgrade", Client: "strazad"}

// The steps a settle of the conversion takes, each with its log line.
const (
	settleMoved = iota
	settleRevised
	settleStatus
	settleInserted
)

// policySettle is one settle of the conversion and the step it takes.
type policySettle struct {
	store.PolicySettle
	step int
}

// convertPolicies settles every policy_sets row to the active snapshot:
// a set the snapshot carries holds status active, its
// published text and that text's priority, a saved edit found in the row
// moves into the set's slot draft, a set the snapshot lacks holds status
// draft, and a set the snapshot carries with no row gets one. It runs at
// boot and at every tick of the checker, under a.configMu. A run whose mark
// equals the one the last settled run read reads nothing more. The mark is
// recorded only when every settle landed, since a settle that answers false
// met live state that moved, and the next run settles it. It writes no
// audit record, because nobody asked for it.
func (a *App) convertPolicies(ctx context.Context) error {
	a.configMu.Lock()
	defer a.configMu.Unlock()
	mark, err := a.store.Drafts().PolicyMark(ctx)
	if err != nil {
		return fmt.Errorf("read the policy mark: %w", err)
	}
	if mark == a.policyMark {
		return nil
	}
	st, err := a.store.Drafts().PolicyState(ctx)
	if err != nil {
		return fmt.Errorf("read the policy sets and the active snapshot: %w", err)
	}
	if st.Snapshot.ID == "" {
		a.log.Warn("policy conversion: no policy snapshot is active, so no stored policy set was settled. Restart strazad, which builds one at boot, if this repeats")
		return nil
	}
	settles, err := a.policySettles(ctx, st)
	if err != nil {
		return err
	}
	landed := true
	for _, s := range settles {
		ok, err := a.store.Drafts().SettlePolicy(ctx, s.PolicySettle)
		if err != nil {
			return fmt.Errorf("settle the policy set %s: %w", s.Name, err)
		}
		if !ok {
			landed = false
			continue
		}
		a.logSettle(ctx, s)
	}
	if landed {
		a.policyMark = st.Mark
	}
	return nil
}

// policySettles answers the settles that make the rows of st equal its
// snapshot, in set name order: the sets the snapshot carries, then the
// rows it lacks.
func (a *App) policySettles(ctx context.Context, st store.PolicyState) ([]policySettle, error) {
	published, err := a.publishedTexts(ctx, st.Snapshot)
	if err != nil {
		return nil, err
	}
	rows := make(map[string]store.PolicySet, len(st.Rows))
	for _, row := range st.Rows {
		rows[row.Name] = row
	}
	names := make([]string, 0, len(published))
	for name := range published {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []policySettle
	for _, name := range names {
		doc := published[name]
		s := policySettle{PolicySettle: store.PolicySettle{Generation: st.Mark.Generation, Snapshot: st.Snapshot.ID, Name: name,
			Status: "active", Text: doc.text, Priority: doc.priority}, step: settleStatus}
		row, ok := rows[name]
		switch {
		case !ok:
			s.step = settleInserted
		case row.YAMLSource != doc.text:
			s.Was = &row
			if err := a.moveSavedEdit(ctx, &s, row.YAMLSource); err != nil {
				return nil, err
			}
		case row.Status != "active" || row.Priority != doc.priority:
			s.Was = &row
		default:
			continue
		}
		out = append(out, s)
	}
	for _, row := range st.Rows {
		if _, ok := published[row.Name]; ok || row.Status == "draft" {
			continue
		}
		out = append(out, policySettle{PolicySettle: store.PolicySettle{Generation: st.Mark.Generation, Snapshot: st.Snapshot.ID,
			Name: row.Name, Was: &row, Status: "draft", Text: row.YAMLSource, Priority: row.Priority}, step: settleStatus})
	}
	return out, nil
}

// moveSavedEdit fills s with the saved edit text found in the row of its set:
// a new slot draft of the upgrade whose item is stamped on the published
// text, or the next revision of the open slot draft, which keeps the base
// its item was stamped with.
func (a *App) moveSavedEdit(ctx context.Context, s *policySettle, text string) error {
	item := drafts.Item{Kind: drafts.KindPolicySet, Name: s.Name, Op: drafts.OpPut, Doc: text}
	s.Item = store.DraftItemRow{Kind: string(item.Kind), Name: s.Name, Op: string(item.Op), Doc: text,
		Base: store.FingerprintPolicySet(store.PolicySet{Name: s.Name, Status: "active", YAMLSource: s.Text}), BaseOp: string(drafts.OpPut), BaseDoc: s.Text}
	s.Rev = store.DraftRevisionRow{Author: upgradeActor, Door: string(drafts.DoorAPI), Digest: revisionDigest([]drafts.Item{item})}
	slot, _, err := a.store.Drafts().BySlot(ctx, "policy:"+s.Name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.Draft, s.step = &store.DraftRow{Door: string(drafts.DoorAPI), Slot: "policy:" + s.Name, Note: savedEditNote}, settleMoved
		return nil
	case err != nil:
		return fmt.Errorf("read the saved edit of the policy set %s: %w", s.Name, err)
	}
	s.Slot, s.SlotRevision, s.step = slot.ID, slot.Revision, settleRevised
	return nil
}

// logSettle writes the one line of a settle that landed. The draft id of
// a new slot draft is read back, since the settle mints it.
func (a *App) logSettle(ctx context.Context, s policySettle) {
	switch s.step {
	case settleMoved:
		id := "unknown"
		if row, _, err := a.store.Drafts().BySlot(ctx, "policy:"+s.Name); err == nil {
			id = strconv.FormatInt(row.ID, 10)
		}
		a.log.Info(fmt.Sprintf("moved the saved edit of policy set %s into draft %s, and the set keeps deciding with its published text", s.Name, id))
	case settleRevised:
		a.log.Info(fmt.Sprintf("moved a newer saved edit of policy set %s into draft %d as revision %d", s.Name, s.Slot, s.SlotRevision+1))
	case settleInserted:
		a.log.Warn(fmt.Sprintf("inserted the missing row of policy set %s from the published snapshot", s.Name))
	default:
		a.log.Info(fmt.Sprintf("set the stored status and priority of policy set %s to match the published snapshot", s.Name))
	}
}

// publishedSet is a set's text as the active snapshot carries it and the
// priority that text declares.
type publishedSet struct {
	text     string
	priority int
}

// publishedTexts opens the snapshot the conversion read with the signing
// keys, as readPolicies does, and answers every set it carries by name.
func (a *App) publishedTexts(ctx context.Context, snap store.Snapshot) (map[string]publishedSet, error) {
	_, open, err := policy.OpenSnapshot(snap.Blob, snap.ID, func(kid string) (ed25519.PublicKey, bool) {
		return a.snapKeys.Public(ctx, kid)
	})
	if err != nil {
		return nil, fmt.Errorf("open the active policy snapshot %s: %w", snap.ID, err)
	}
	out := make(map[string]publishedSet, len(open.Documents))
	for _, raw := range open.Documents {
		doc, err := policy.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("a set in the active policy snapshot %s does not parse: %w", snap.ID, err)
		}
		out[doc.Metadata.Name] = publishedSet{text: string(raw), priority: doc.Spec.Priority}
	}
	return out, nil
}
