package ledger

// ArtifactLookup is what an ingest pass's raw usage signals get resolved
// against: every artifact path currently known to the ledger, and skill
// names mapped to their path (skills are the one kind #39's signals name
// rather than path directly - see internal/ingest, SkillInvocations).
// Fetched once per ingest batch, not once per run, since it does not change
// mid-batch and the artifacts table is small (single-digit megabytes,
// design doc "Ledger").
type ArtifactLookup struct {
	Paths      map[string]bool
	SkillNames map[string]string
}

// BuildArtifactLookup reads the current artifacts table into an
// ArtifactLookup. An artifact discovery has never found (the artifacts
// table is empty, e.g. `loom advise` has never run) resolves every usage
// signal to nothing - #39's join needs discovery to have run at least once,
// same precondition #38's staleness check already has.
func (d *DB) BuildArtifactLookup() (ArtifactLookup, error) {
	l := ArtifactLookup{Paths: map[string]bool{}, SkillNames: map[string]string{}}
	rows, err := d.sql.Query(`SELECT type, path, name FROM artifacts`)
	if err != nil {
		return l, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var kind, path, name string
		if err := rows.Scan(&kind, &path, &name); err != nil {
			return l, err
		}
		l.Paths[path] = true
		if kind == "skill" {
			l.SkillNames[name] = path
		}
	}
	return l, rows.Err()
}

// Resolve turns raw ingest signals into a path -> uses map ready for
// ReplaceArtifactUsage. A signal that does not match anything currently
// known (a skill invoked under a name Loom has not discovered, a file
// outside any known artifact's path) is dropped rather than guessed at -
// #39 answers "was a known artifact used", not "what files exist".
func (l ArtifactLookup) Resolve(skillTouches, fileTouches map[string]int) map[string]int {
	out := map[string]int{}
	for name, n := range skillTouches {
		if path, ok := l.SkillNames[name]; ok {
			out[path] += n
		}
	}
	for path, n := range fileTouches {
		if l.Paths[path] {
			out[path] += n
		}
	}
	return out
}

// ReplaceArtifactUsage writes runID's artifact_usage rows, replacing
// whatever was there before - same full-replace contract as
// ReplaceToolUsage (B7b), since usage is recomputed from a fresh read of
// the file on every ingest.
func (d *DB) ReplaceArtifactUsage(runID int64, usage map[string]int) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM artifact_usage WHERE run_id = ?`, runID); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO artifact_usage (run_id, artifact_path, uses) VALUES (?, ?, ?)`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for path, n := range usage {
		if _, err := stmt.Exec(runID, path, n); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ArtifactUsageSummary is one artifact's aggregate usage: how many times a
// run touched it, and the most recent run's own timestamp (started_at,
// falling back to ended_at for a run with no recorded start). Empty
// LastUsedAt means never touched under usage tracking - #38's "is anyone
// using it" is false, which is a different claim from "not on disk".
type ArtifactUsageSummary struct {
	Uses       int
	LastUsedAt string
}

// UsageSummary aggregates artifact_usage across every run, joined against
// runs for the timestamp. An artifact path with zero usage rows simply does
// not appear in the result - callers must treat absence as "never used",
// not as a zero-value summary, which is why this returns a map rather than
// one row per known artifact.
func (d *DB) UsageSummary() (map[string]ArtifactUsageSummary, error) {
	rows, err := d.sql.Query(`
		SELECT au.artifact_path, SUM(au.uses),
		       MAX(COALESCE(r.started_at, r.ended_at, ''))
		FROM artifact_usage au JOIN runs r ON r.id = au.run_id
		GROUP BY au.artifact_path`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := map[string]ArtifactUsageSummary{}
	for rows.Next() {
		var path string
		var s ArtifactUsageSummary
		if err := rows.Scan(&path, &s.Uses, &s.LastUsedAt); err != nil {
			return nil, err
		}
		out[path] = s
	}
	return out, rows.Err()
}
