package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// Revoke ends the sessions of ids whose row is not revoked yet. The status
// write, the revocation rows and the event share one transaction, so a
// database that refuses any of them leaves every row as it was and a rerun
// finds the same sessions still to end. The WHERE clause, not RowsAffected,
// tells an ended row from a replay, because both dialects count a
// same-value write as affected. The row is the replay signal on purpose: a
// restarted or peer replica rebuilds its denylist from these rows.
func (r sessionRepo) Revoke(ctx context.Context, ids []string, reason string, event func(ended []string) (OutboxEvent, error)) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var ended []string
	err := r.s.tx(ctx, func(tx *sql.Tx) error {
		// The status rides twice: the dialect rewriter takes $1..$n strictly
		// in order.
		args := []any{SessionRevoked, SessionRevoked}
		ph := make([]string, len(ids))
		for i, id := range ids {
			ph[i] = "$" + strconv.Itoa(i+3)
			args = append(args, id)
		}
		changed, err := r.s.returnedIDs(ctx, tx, `UPDATE sessions SET status = $1 WHERE status <> $2 AND id IN (`+
			strings.Join(ph, ", ")+`) RETURNING id`, args...)
		if err != nil {
			return err
		}
		// Input order, each id once, so a repeated id ends and counts once.
		for _, id := range ids {
			if changed[id] {
				ended = append(ended, id)
				delete(changed, id)
			}
		}
		if len(ended) == 0 {
			return nil
		}
		ev, err := event(ended)
		if err != nil {
			return err
		}
		// The event names the sessions this transaction ended, so it is
		// made and bounded inside it. Only an admin's reason can make it long.
		ev.CE = boundRecord(ev.CE)
		return r.s.writeRevocation(ctx, tx, RevokeSession, ended, reason, ev)
	})
	if err != nil {
		return nil, err
	}
	return ended, nil
}

// Revoke deletes the device row, writes its revocation row and writes event,
// in one transaction, so a refused write leaves the device enrolled.
func (r deviceRepo) Revoke(ctx context.Context, id, reason string, event OutboxEvent) error {
	event.CE = boundRecord(event.CE)
	return r.s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, r.s.q(`DELETE FROM devices WHERE id = $1`), id)
		if err := mustAffect(res, r.s.mapErr(err)); err != nil {
			return err
		}
		return r.s.writeRevocation(ctx, tx, RevokeDevice, []string{id}, reason, event)
	})
}

// returnedIDs runs an UPDATE ... RETURNING id inside tx and answers the set
// of ids it returned. The rows are closed before it returns, because a
// connection runs the next statement of the transaction only then.
func (s *sqlStore) returnedIDs(ctx context.Context, tx *sql.Tx, query string, args ...any) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, s.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, s.mapErr(rows.Err())
}

// writeRevocation writes, inside tx, one revocation row of kind with reason
// and origin admin for each target, then the outbox event ev, unpublished,
// which the caller has bounded.
// Six parameters per row keep 1000 targets far inside both drivers' limits.
func (s *sqlStore) writeRevocation(ctx context.Context, tx *sql.Tx, kind string, targets []string, reason string, ev OutboxEvent) error {
	at := s.tArg(now())
	values := make([]string, len(targets))
	args := make([]any, 0, 6*len(targets))
	for i, id := range targets {
		n := 6 * i
		values[i] = fmt.Sprintf("($%d, $%d, $%d, $%d, $%d, $%d)", n+1, n+2, n+3, n+4, n+5, n+6)
		args = append(args, newID(), kind, id, reason, RevocationOriginAdmin, at)
	}
	if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO revocations (id, kind, target_id, reason, origin, created_at) VALUES `+
		strings.Join(values, ", ")), args...); err != nil {
		return s.mapErr(err)
	}
	if ev.ID == "" {
		ev.ID = newID()
	}
	_, err := tx.ExecContext(ctx, s.q(`INSERT INTO events_outbox (id, subject, ce, published, attempts, created_at)
		VALUES ($1, $2, $3, FALSE, 0, $4)`), ev.ID, ev.Subject, ev.CE, s.tArg(now()))
	return s.mapErr(err)
}
