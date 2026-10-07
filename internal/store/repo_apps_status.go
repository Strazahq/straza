package store

import "context"

// SetStatus writes an app row's runtime status and its updated_at, and no
// other column. Every replica reports its own instance's health into the one
// row, so a replica whose copy of the row is older must not write back a
// manifest, version or source that another replica changed. It answers
// ErrNotFound when no live row has the id.
func (r appRepo) SetStatus(ctx context.Context, id, status string) error {
	return mustAffect(r.s.exec(ctx, `UPDATE apps SET status = $1, updated_at = $2 WHERE id = $3 AND deleted_at IS NULL`,
		status, r.s.tArg(now()), id))
}
