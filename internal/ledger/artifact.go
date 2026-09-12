package ledger

import "time"

// ArtifactRecord mirrors internal/artifact.Artifact for the subset the
// ledger stores. The ledger package doesn't import internal/artifact — the
// caller (cmd/loom) does the conversion — so ledger stays the leaf package
// with no dependency on artifact discovery's own scanning logic.
type ArtifactRecord struct {
	Kind        string
	Path        string
	Name        string
	Description string
}

// UpsertArtifact records that an artifact at Path was seen at seenAt: insert
// it with status "active" if new, or bump last_seen (and status back to
// "active", in case a prior pass had marked it stale) if already known.
// Never touches first_seen on an existing row — that field's whole purpose is
// to record when the artifact was first discovered, not when it was last
// re-confirmed. Name and description are refreshed every call, since a
// skill's own frontmatter is more current than whatever the ledger last
// stored.
func (d *DB) UpsertArtifact(a ArtifactRecord, seenAt time.Time) error {
	ts := formatTime(seenAt)
	_, err := d.sql.Exec(`
		INSERT INTO artifacts (type, path, name, description, status, first_seen, last_seen)
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

// MarkStaleArtifacts sets status = 'stale' on every artifact whose last_seen
// predates cutoff — run after a full discovery pass, so an artifact that
// used to exist but no longer does (deleted skill, removed hook) is flagged
// rather than left claiming to be active forever. Never deletes the row:
// history (first_seen, and whatever events reference it) stays intact per
// design doc constraint 8, "never write to human-authored files" — this
// writes only to Loom's own table, and only changes a status column, not the
// row's identity.
func (d *DB) MarkStaleArtifacts(cutoff time.Time) error {
	_, err := d.sql.Exec(`UPDATE artifacts SET status = 'stale' WHERE last_seen < ? AND status != 'stale'`, formatTime(cutoff))
	return err
}

// ListArtifacts returns every artifact row, most recently seen first.
func (d *DB) ListArtifacts() ([]ArtifactRow, error) {
	rows, err := d.sql.Query(`SELECT type, path, name, description, status, first_seen, last_seen FROM artifacts ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ArtifactRow
	for rows.Next() {
		var r ArtifactRow
		if err := rows.Scan(&r.Kind, &r.Path, &r.Name, &r.Description, &r.Status, &r.FirstSeen, &r.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ArtifactRow is one row of the artifacts table as read back.
type ArtifactRow struct {
	Kind        string
	Path        string
	Name        string
	Description string
	Status      string
	FirstSeen   string
	LastSeen    string
}
