package store

import (
	"context"
	"time"
)

type approverRepo struct{ s *sqlStore }

// --- devices ---

const approverDeviceCols = "id, user_id, name, platform, key_alg, public_key, " +
	"key_security_level, attestation_kind, attestation_blob, created_at, last_seen_at"

func scanApproverDevice(row scanner) (ApproverDevice, error) {
	var d ApproverDevice
	var ca scanTime
	var ls scanTimePtr
	if err := row.Scan(&d.ID, &d.UserID, &d.Name, &d.Platform, &d.KeyAlg, &d.PublicKey,
		&d.KeySecurityLevel, &d.AttestationKind, &d.AttestationBlob, &ca, &ls); err != nil {
		return ApproverDevice{}, scanErr(err)
	}
	d.CreatedAt, d.LastSeenAt = ca.t, ls.t
	return d, nil
}

func (r approverRepo) InsertDevice(ctx context.Context, d ApproverDevice) (ApproverDevice, error) {
	if d.ID == "" {
		d.ID = "apd_" + newID()
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now()
	}
	if d.AttestationKind == "" {
		d.AttestationKind = "none"
	}
	_, err := r.s.exec(ctx, `INSERT INTO approver_devices
		(id, user_id, name, platform, key_alg, public_key, key_security_level,
		 attestation_kind, attestation_blob, created_at, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		d.ID, d.UserID, d.Name, d.Platform, d.KeyAlg, d.PublicKey, d.KeySecurityLevel,
		d.AttestationKind, d.AttestationBlob, r.s.tArg(d.CreatedAt), r.s.tArgPtr(d.LastSeenAt))
	if err != nil {
		return ApproverDevice{}, err
	}
	return d, nil
}

func (r approverRepo) GetDevice(ctx context.Context, id string) (ApproverDevice, error) {
	return scanApproverDevice(r.s.queryRow(ctx,
		`SELECT `+approverDeviceCols+` FROM approver_devices WHERE id = $1`, id))
}

// ListDevices returns enrolled devices (optionally one user's) in enrolment
// order, each with its push-registration count. The count rides a scalar
// subquery rather than a JOIN + GROUP BY so the same SQL is unambiguous on
// both dialects; the table is phone-sized. Admin/control-plane surface only,
// never a request path.
func (r approverRepo) ListDevices(ctx context.Context, userID string) ([]ApproverDeviceInfo, error) {
	query := `SELECT ` + approverDeviceCols + `,
		(SELECT COUNT(*) FROM approver_push p WHERE p.device_id = approver_devices.id)
		FROM approver_devices`
	var args []any
	if userID != "" {
		query += ` WHERE user_id = $1`
		args = append(args, userID)
	}
	query += ` ORDER BY created_at`
	rows, err := r.s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ApproverDeviceInfo
	for rows.Next() {
		var info ApproverDeviceInfo
		var ca scanTime
		var ls scanTimePtr
		if err := rows.Scan(&info.ID, &info.UserID, &info.Name, &info.Platform, &info.KeyAlg,
			&info.PublicKey, &info.KeySecurityLevel, &info.AttestationKind, &info.AttestationBlob,
			&ca, &ls, &info.PushRoutes); err != nil {
			return nil, scanErr(err)
		}
		info.CreatedAt, info.LastSeenAt = ca.t, ls.t
		out = append(out, info)
	}
	return out, rows.Err()
}

func (r approverRepo) DeleteDevice(ctx context.Context, id string) error {
	return mustAffect(r.s.exec(ctx, `DELETE FROM approver_devices WHERE id = $1`, id))
}

func (r approverRepo) TouchDevice(ctx context.Context, id string, at time.Time) error {
	_, err := r.s.exec(ctx, `UPDATE approver_devices SET last_seen_at = $1 WHERE id = $2`,
		r.s.tArg(at), id)
	return err
}

// --- enroll tokens ---

func (r approverRepo) InsertEnrollToken(ctx context.Context, t ApproverEnrollToken) (ApproverEnrollToken, error) {
	if t.ID == "" {
		t.ID = newID()
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now()
	}
	_, err := r.s.exec(ctx, `INSERT INTO approver_enroll_tokens
		(id, token_hash, user_id, channel, created_at, expires_at, used_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		t.ID, t.TokenHash, t.UserID, t.Channel, r.s.tArg(t.CreatedAt), r.s.tArg(t.ExpiresAt), r.s.tArgPtr(t.UsedAt))
	if err != nil {
		return ApproverEnrollToken{}, err
	}
	return t, nil
}

// ConsumeEnrollToken flips one unused, unexpired row to used and returns it.
// The guarded UPDATE is the one-time gate: only the caller whose UPDATE affects
// a row may enroll; a second attempt finds used_at already set and gets
// ErrNotFound. Two statements (write then read) are fine on the control plane.
func (r approverRepo) ConsumeEnrollToken(ctx context.Context, tokenHash string, at time.Time) (ApproverEnrollToken, error) {
	res, err := r.s.exec(ctx, `UPDATE approver_enroll_tokens SET used_at = $1
		WHERE token_hash = $2 AND used_at IS NULL AND expires_at > $3`,
		r.s.tArg(at), tokenHash, r.s.tArg(at))
	if err != nil {
		return ApproverEnrollToken{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ApproverEnrollToken{}, ErrNotFound
	}
	var t ApproverEnrollToken
	var ca, ea scanTime
	var ua scanTimePtr
	if err := r.s.queryRow(ctx,
		`SELECT id, token_hash, user_id, channel, created_at, expires_at, used_at
		 FROM approver_enroll_tokens WHERE token_hash = $1`, tokenHash).
		Scan(&t.ID, &t.TokenHash, &t.UserID, &t.Channel, &ca, &ea, &ua); err != nil {
		return ApproverEnrollToken{}, scanErr(err)
	}
	t.CreatedAt, t.ExpiresAt, t.UsedAt = ca.t, ea.t, ua.t
	return t, nil
}

func (r approverRepo) PurgeEnrollTokensBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.s.exec(ctx, `DELETE FROM approver_enroll_tokens WHERE expires_at < $1`, r.s.tArg(cutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// --- challenges ---

func (r approverRepo) InsertChallenge(ctx context.Context, c ApproverChallenge) error {
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now()
	}
	_, err := r.s.exec(ctx, `INSERT INTO approver_challenges
		(challenge, device_id, approval_id, created_at, expires_at, used_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		c.Challenge, c.DeviceID, c.ApprovalID, r.s.tArg(c.CreatedAt), r.s.tArg(c.ExpiresAt), r.s.tArgPtr(c.UsedAt))
	return err
}

// ConsumeChallenge is the single-use freshness gate: the guarded UPDATE claims
// exactly one unused, unexpired row bound to (device, approval). A replay or a
// stale nonce affects zero rows → won=false.
func (r approverRepo) ConsumeChallenge(ctx context.Context, challenge, deviceID, approvalID string, at time.Time) (bool, error) {
	res, err := r.s.exec(ctx, `UPDATE approver_challenges SET used_at = $1
		WHERE challenge = $2 AND device_id = $3 AND approval_id = $4
		  AND used_at IS NULL AND expires_at > $5`,
		r.s.tArg(at), challenge, deviceID, approvalID, r.s.tArg(at))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (r approverRepo) PurgeChallengesBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.s.exec(ctx, `DELETE FROM approver_challenges WHERE expires_at < $1`, r.s.tArg(cutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// --- push ---

// UpsertPush inserts a registration; a dedupe-triple collision REFRESHES the
// row's registered_at instead of erroring. The refresh is load-bearing, not
// bookkeeping: the app re-PUTs on every launch and token rotation, so the
// timestamp means "the app last confirmed this token", and DeletePushBefore
// compares it against upstream invalidation horizons (the APNs 410 race).
func (r approverRepo) UpsertPush(ctx context.Context, p ApproverPush) error {
	if p.ID == "" {
		p.ID = newID()
	}
	if p.RegisteredAt.IsZero() {
		p.RegisteredAt = now()
	}
	_, err := r.s.exec(ctx, `INSERT INTO approver_push
		(id, device_id, kind, token_or_endpoint, created_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (device_id, kind, token_or_endpoint)
		DO UPDATE SET created_at = excluded.created_at`,
		p.ID, p.DeviceID, p.Kind, p.TokenOrEndpoint, r.s.tArg(p.RegisteredAt))
	return err
}

// DeletePushBefore deletes ONE registration iff its registered_at is not newer
// than notAfter: the guarded prune for upstream invalidations that carry a
// timestamp horizon (APNs 410 Unregistered, and the send-attempt start for
// reasons without one). A device that re-confirmed the registration after the
// horizon survives; an unconditional prune would silently un-ring a
// re-registered phone forever. The bool reports whether a row was actually
// deleted, so the caller's log tells prune from guard-skip honestly.
func (r approverRepo) DeletePushBefore(ctx context.Context, deviceID, kind, tokenOrEndpoint string, notAfter time.Time) (bool, error) {
	res, err := r.s.exec(ctx, `DELETE FROM approver_push
		WHERE device_id = $1 AND kind = $2 AND token_or_endpoint = $3 AND created_at <= $4`,
		deviceID, kind, tokenOrEndpoint, r.s.tArg(notAfter))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (r approverRepo) DeletePush(ctx context.Context, deviceID, kind, tokenOrEndpoint string) error {
	_, err := r.s.exec(ctx, `DELETE FROM approver_push
		WHERE device_id = $1 AND kind = $2 AND token_or_endpoint = $3`,
		deviceID, kind, tokenOrEndpoint)
	return err
}

// ListPushTargets joins every push registration to its owning user via the
// device row. The join is inner, so a registration whose device was revoked
// (row deleted) is silently excluded; the routing side never delivers to it.
func (r approverRepo) ListPushTargets(ctx context.Context) ([]ApproverPushTarget, error) {
	rows, err := r.s.query(ctx, `SELECT p.id, p.device_id, p.kind, p.token_or_endpoint, p.created_at, d.user_id
		FROM approver_push p JOIN approver_devices d ON d.id = p.device_id
		ORDER BY p.created_at`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ApproverPushTarget
	for rows.Next() {
		var t ApproverPushTarget
		var ca scanTime
		if err := rows.Scan(&t.ID, &t.DeviceID, &t.Kind, &t.TokenOrEndpoint, &ca, &t.UserID); err != nil {
			return nil, scanErr(err)
		}
		t.RegisteredAt = ca.t
		out = append(out, t)
	}
	return out, rows.Err()
}

// CountDevices reports the number of enrolled approver devices.
func (r approverRepo) CountDevices(ctx context.Context) (int, error) {
	var n int
	if err := r.s.queryRow(ctx, `SELECT COUNT(*) FROM approver_devices`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
