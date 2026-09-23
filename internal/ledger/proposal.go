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
	// ProposalWithdrawn means the generator stopped producing this proposal -
	// the evidence it rested on no longer holds. Kept distinct from dismissed
	// for the same reason applied is: "the situation changed" is a different
	// event from "a human rejected it", and collapsing them loses the thing
	// the ledger is for (issue #40).
	ProposalWithdrawn = "withdrawn"
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
//   - no row for this (kind, subject), or the existing row is withdrawn:
//     insert/revive as pending, subject to the pending cap - see below for
//     why withdrawn is grouped with "no row" rather than with dismissed
//   - row exists (pending, dismissed, or applied), same evidence: leave it
//     completely alone, which is what preserves a dismissal
//   - row exists, evidence changed: replace it and raise it again
//
// Withdrawn is deliberately not "sticky" the way dismissed is: a dismissal
// is a human's decision and must survive until the evidence changes, but a
// withdrawal is only ever the generator's own opinion, and if the generator
// is standing behind the same proposal again, it revives. Treating a
// withdrawn row as equivalent to no row at all - re-raised subject to the
// same cap a brand new proposal would face, not given a free pass - is what
// makes that true without letting a burst of revivals silently exceed
// MaxPendingProposals. Found empirically by code review, before this
// shipped: without this, a real, unchanged finding could never come back
// once withdrawn even once.
//
// Re-raise on change, never on a timer. Reports whether a row was written.
func (d *DB) UpsertProposal(p ProposalRow, at time.Time) (bool, error) {
	var existingHash, existingStatus string
	err := d.sql.QueryRow(`SELECT evidence_hash, status FROM proposals WHERE kind = ? AND subject = ?`,
		p.Kind, p.Subject).Scan(&existingHash, &existingStatus)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}

	consumesNewSlot := errors.Is(err, sql.ErrNoRows) || existingStatus == ProposalWithdrawn
	if !consumesNewSlot && existingHash == p.EvidenceHash {
		return false, nil
	}
	if consumesNewSlot {
		// The pending cap applies only here: re-raising an already-pending
		// row (dismissed or applied, evidence changed) must never be
		// blocked by a full queue, or a changed fact would be silently
		// dropped. A new or revived row is different - it is about to start
		// occupying a pending slot that did not count against the cap a
		// moment ago, so it has to clear the same bar a brand new proposal
		// would.
		n, err := d.CountPendingProposals()
		if err != nil {
			return false, err
		}
		if n >= MaxPendingProposals {
			return false, nil
		}
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

// ProposalIdentity is a proposal's dedupe key - the same (kind, subject)
// pair the `proposals` table's own UNIQUE constraint keys on. A struct
// rather than a delimited string: subject is a raw filesystem path for
// several kinds and can legally contain any byte a POSIX filename can,
// including one a string-concatenation key might have used as its own
// delimiter.
type ProposalIdentity struct {
	Kind    string
	Subject string
}

// WithdrawStalePending marks pending proposals withdrawn when the generator
// no longer produces them - the case UpsertProposal's dedupe rule leaves
// silent (evidence gone, not changed, so nothing re-raises and nothing
// removes it either). generated is every (kind, subject) the current run
// actually produced; any row still status=pending but not in that set has
// outlived its own evidence.
//
// Dismissed and applied rows are untouched - a proposal a human already
// acted on is not this function's concern either way.
//
// Callers should run this before re-upserting the generated set, not after:
// MaxPendingProposals' cap (in UpsertProposal) counts status=pending rows, so
// withdrawing stale ones first is what lets a newly-generated proposal use
// the slot a now-stale one just gave up, in the same pass (issue #40's
// second half: "the cap should count only proposals the generator still
// stands behind").
func (d *DB) WithdrawStalePending(generated map[ProposalIdentity]bool) error {
	pending, err := d.ListProposals(true)
	if err != nil {
		return err
	}
	var stale []int64
	for _, p := range pending {
		if !generated[ProposalIdentity{Kind: p.Kind, Subject: p.Subject}] {
			stale = append(stale, p.ID)
		}
	}
	if len(stale) == 0 {
		return nil
	}

	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`UPDATE proposals SET status = ? WHERE id = ?`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, id := range stale {
		if _, err := stmt.Exec(ProposalWithdrawn, id); err != nil {
			return err
		}
	}
	return tx.Commit()
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
