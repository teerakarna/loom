package ledger

import (
	"database/sql"
	"errors"
	"time"
)

// Proposal statuses.
const (
	ProposalPending   = "pending"
	ProposalDismissed = "dismissed"
	// ProposalApplied means the change was made. Kept distinct from dismissed
	// so the record says what happened, not merely that it stopped being
	// shown.
	ProposalApplied = "applied"
)

// MaxPendingProposals bounds how many proposals can be waiting at once.
// Constraint 10: an advisor that produces proposals faster than a human accepts
// them recreates exactly the fatigue this project exists to reduce, so the
// generator refuses to add more rather than growing without limit.
const MaxPendingProposals = 20

// ProposalRow is one proposal as stored. Subject is what it is about (an
// asset path, an agent type); EvidenceHash is what makes a dismissal stick
// until the underlying facts actually change.
type ProposalRow struct {
	ID           int64
	Kind         string
	Subject      string
	Evidence     string // JSON
	EvidenceHash string
	SampleSize   int
	EffectSize   *float64
	Status       string
	CreatedAt    string
}

// UpsertProposal records one proposal, with the dedupe rule that makes this
// bearable to live with:
//
//   - no row for this (kind, subject): insert as pending
//   - row exists, same evidence: leave it completely alone, which is what
//     preserves a dismissal
//   - row exists, evidence changed: replace it and raise it again
//
// Re-raise on change, never on a timer. Reports whether a row was written.
func (d *DB) UpsertProposal(p ProposalRow, at time.Time) (bool, error) {
	var existingHash, existingStatus string
	err := d.sql.QueryRow(`SELECT evidence_hash, status FROM proposals WHERE kind = ? AND subject = ?`,
		p.Kind, p.Subject).Scan(&existingHash, &existingStatus)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		// New subject. The pending cap applies only here: re-raising an
		// existing proposal must never be blocked by a full queue, or a
		// changed fact would be silently dropped.
		n, err := d.CountPendingProposals()
		if err != nil {
			return false, err
		}
		if n >= MaxPendingProposals {
			return false, nil
		}
	case err != nil:
		return false, err
	case existingHash == p.EvidenceHash:
		// Nothing has changed. A dismissal stays dismissed.
		return false, nil
	}

	_, err = d.sql.Exec(`
		INSERT INTO proposals (kind, subject, evidence, evidence_hash, sample_size, effect_size, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(kind, subject) DO UPDATE SET
			evidence = excluded.evidence,
			evidence_hash = excluded.evidence_hash,
			sample_size = excluded.sample_size,
			effect_size = excluded.effect_size,
			status = excluded.status,
			created_at = excluded.created_at`,
		p.Kind, p.Subject, p.Evidence, p.EvidenceHash, p.SampleSize, p.EffectSize,
		ProposalPending, formatTime(at))
	if err != nil {
		return false, err
	}
	return true, nil
}

// CountPendingProposals reports how many proposals are currently waiting.
func (d *DB) CountPendingProposals() (int, error) {
	var n int
	err := d.sql.QueryRow(`SELECT COUNT(*) FROM proposals WHERE status = ?`, ProposalPending).Scan(&n)
	return n, err
}

// GetProposal returns one proposal by id.
func (d *DB) GetProposal(id int64) (*ProposalRow, error) {
	var r ProposalRow
	err := d.sql.QueryRow(`
		SELECT id, kind, subject, evidence, evidence_hash, sample_size, effect_size, status, created_at
		FROM proposals WHERE id = ?`, id).
		Scan(&r.ID, &r.Kind, &r.Subject, &r.Evidence, &r.EvidenceHash,
			&r.SampleSize, &r.EffectSize, &r.Status, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// MarkProposalApplied records that a proposal was acted on.
func (d *DB) MarkProposalApplied(id int64) error {
	_, err := d.sql.Exec(`UPDATE proposals SET status = ? WHERE id = ?`, ProposalApplied, id)
	return err
}

// DismissProposal marks one dismissed. It stays dismissed until its evidence
// changes (see UpsertProposal), rather than until some interval elapses.
func (d *DB) DismissProposal(id int64) error {
	res, err := d.sql.Exec(`UPDATE proposals SET status = ? WHERE id = ?`, ProposalDismissed, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("no proposal with that id")
	}
	return nil
}

// ListProposals returns proposals, newest first. pendingOnly filters to those
// still waiting.
func (d *DB) ListProposals(pendingOnly bool) ([]ProposalRow, error) {
	q := `SELECT id, kind, subject, evidence, evidence_hash, sample_size, effect_size, status, created_at
	      FROM proposals`
	var args []any
	if pendingOnly {
		q += ` WHERE status = ?`
		args = append(args, ProposalPending)
	}
	q += ` ORDER BY id DESC`

	rows, err := d.sql.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []ProposalRow
	for rows.Next() {
		var r ProposalRow
		if err := rows.Scan(&r.ID, &r.Kind, &r.Subject, &r.Evidence, &r.EvidenceHash,
			&r.SampleSize, &r.EffectSize, &r.Status, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
