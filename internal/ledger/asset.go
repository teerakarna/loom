package ledger

import "time"

// AssetRecord mirrors internal/asset.Asset for the subset the
// ledger stores. The ledger package doesn't import internal/asset, the
// caller (cmd/loom) does the conversion, so ledger stays the leaf package
// with no dependency on asset discovery's own scanning logic.
type AssetRecord struct {
	Kind        string
	Path        string
	Name        string
	Description string
}

// UpsertAsset records that an asset at Path was seen at seenAt: insert
// it with status "active" if new, or bump last_seen (and status back to
// "active", in case a prior pass had marked it stale) if already known.
// Never touches first_seen on an existing row, that field's whole purpose is
// to record when the asset was first discovered, not when it was last
// re-confirmed. Name and description are refreshed every call, since a
// skill's own frontmatter is more current than whatever the ledger last
// stored.
func (d *DB) UpsertAsset(a AssetRecord, seenAt time.Time) error {
	ts := formatTime(seenAt)
	_, err := d.sql.Exec(`
		INSERT INTO assets (type, path, name, description, status, first_seen, last_seen)
		VALUES (?, ?, ?, ?, 'active', ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			type = excluded.type,
			name = excluded.name,
			description = excluded.description,
			status = 'active',
			last_seen = excluded.last_seen`,
		a.Kind, a.Path, a.Name, a.Description, ts, ts,
	)
	return err
}

// MarkStaleAssets sets status = 'stale' on every asset whose last_seen
// predates cutoff, run after a full discovery pass, so an asset that
// used to exist but no longer does (deleted skill, removed hook) is flagged
// rather than left claiming to be active forever. Never deletes the row:
// history (first_seen, and whatever events reference it) stays intact per
// design doc constraint 8, "never write to human-authored files", this
// writes only to Loom's own table, and only changes a status column, not the
// row's identity.
func (d *DB) MarkStaleAssets(cutoff time.Time) error {
	_, err := d.sql.Exec(`UPDATE assets SET status = 'stale' WHERE last_seen < ? AND status != 'stale'`, formatTime(cutoff))
	return err
}

// ListAssets returns every asset row, most recently seen first.
func (d *DB) ListAssets() ([]AssetRow, error) {
	rows, err := d.sql.Query(`SELECT type, path, name, description, status, first_seen, last_seen FROM assets ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []AssetRow
	for rows.Next() {
		var r AssetRow
		if err := rows.Scan(&r.Kind, &r.Path, &r.Name, &r.Description, &r.Status, &r.FirstSeen, &r.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AssetRow is one row of the assets table as read back.
type AssetRow struct {
	Kind        string
	Path        string
	Name        string
	Description string
	Status      string
	FirstSeen   string
	LastSeen    string
}
