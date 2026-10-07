package store

import (
	"context"
	"sort"
)

func (r draftRepo) BySource(ctx context.Context, source string) ([]DraftRow, error) {
	if source == "" {
		return nil, nil
	}
	return r.drafts(ctx, draftSelect+` WHERE d.source = $1 ORDER BY d.id DESC`, source)
}

func (r draftRepo) FileDecisions(ctx context.Context) ([]FileDecision, error) {
	rows, err := r.s.query(ctx, `SELECT d.id, d.door, d.state, d.source, d.decided_at, d.decided_by_id, d.decided_by_name,
		d.decided_via, d.decided_client, i.seq, i.kind, i.name, i.op
		FROM drafts d JOIN draft_items i ON i.draft_id = d.id AND i.kind = 'App'
		WHERE (d.door = 'apps-directory' AND d.state IN ('published', 'discarded'))
		OR (d.state = 'published' AND EXISTS (SELECT 1 FROM draft_items x WHERE x.draft_id = d.id AND x.kind = 'App' AND x.op = 'remove'))
		ORDER BY d.id, i.seq`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []FileDecision
	for rows.Next() {
		var f FileDecision
		var at scanTimePtr
		var it DraftItemRow
		by := &f.DecidedBy
		if err := rows.Scan(&f.DraftID, &f.Door, &f.State, &f.Source, &at, &by.ID, &by.Name, &by.Via, &by.Client,
			&it.Seq, &it.Kind, &it.Name, &it.Op); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].DraftID == f.DraftID {
			out[n-1].Items = append(out[n-1].Items, it)
			continue
		}
		if at.t != nil {
			f.DecidedAt = *at.t
		}
		f.Items = []DraftItemRow{it}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The order is taken here, because sqlite keeps the decision time as
	// RFC3339Nano text, whose trailing zeros are dropped, so its text order
	// is not always the order in time.
	sort.SliceStable(out, func(i, j int) bool { return out[i].DecidedAt.Before(out[j].DecidedAt) })
	return out, nil
}
