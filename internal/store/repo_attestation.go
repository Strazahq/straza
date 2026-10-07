package store

import "context"

type attestationRepo struct{ s *sqlStore }

const attestationCols = "id, artifact, harness, platform, hash, note, created_at"

func scanAttestationHash(row scanner) (AttestationHash, error) {
	var h AttestationHash
	var ca scanTime
	if err := row.Scan(&h.ID, &h.Artifact, &h.Harness, &h.Platform, &h.Hash, &h.Note, &ca); err != nil {
		return AttestationHash{}, scanErr(err)
	}
	h.CreatedAt = ca.t
	return h, nil
}

func (r attestationRepo) Create(ctx context.Context, h AttestationHash) (AttestationHash, error) {
	h.ID = newID()
	h.CreatedAt = now()
	_, err := r.s.exec(ctx, `INSERT INTO attestation_hashes (id, artifact, harness, platform, hash, note, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		h.ID, h.Artifact, h.Harness, h.Platform, h.Hash, h.Note, r.s.tArg(h.CreatedAt))
	if err != nil {
		return AttestationHash{}, err
	}
	return h, nil
}

func (r attestationRepo) List(ctx context.Context) ([]AttestationHash, error) {
	rows, err := r.s.query(ctx,
		`SELECT `+attestationCols+` FROM attestation_hashes ORDER BY artifact, created_at`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []AttestationHash
	for rows.Next() {
		h, err := scanAttestationHash(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (r attestationRepo) Delete(ctx context.Context, id string) error {
	return mustAffect(r.s.exec(ctx, `DELETE FROM attestation_hashes WHERE id = $1`, id))
}
