package ledger

// ProposalRow is one row of the proposals table as read back. The table is
// schema-complete from B1 (see ledger.go) but nothing writes to it until B5
// ("Advisor proposals for promotion and retirement" — docs/design.md,
// Phasing). ListProposals exists in B2 so the MCP server's list_proposals
// tool has something real to call — it will correctly return an empty list
// until B5 lands, which is the honest answer, not a stub.
type ProposalRow struct {
	ID         int64
	Kind       string
	Evidence   string
	SampleSize int
	EffectSize *float64
	Status     string
	CreatedAt  string
}

// ListProposals returns every proposal, most recent first.
func (d *DB) ListProposals() ([]ProposalRow, error) {
	rows, err := d.sql.Query(`SELECT id, kind, evidence, sample_size, effect_size, status, created_at FROM proposals ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []ProposalRow
	for rows.Next() {
		var r ProposalRow
		if err := rows.Scan(&r.ID, &r.Kind, &r.Evidence, &r.SampleSize, &r.EffectSize, &r.Status, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
