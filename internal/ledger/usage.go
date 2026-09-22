package ledger

import (
	"github.com/teerakarna/loom/internal/ingest"
)

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
//
// Ordered by path so that two artifacts sharing a name (a project-level
// skill overriding a global one of the same name - discover.go scans both
// home and cwd skill dirs, and UpsertArtifact keys on path, not name, so
// both persist as separate rows) resolve to a deterministic one rather than
// whichever row SQLite happened to return last. Which one wins is still a
// coin flip in effect (path order, not "the more specific one"), but a
// stable coin only flipped once, not a fresh one on every query - a real
// name collision is a separate finding worth its own fix, not silently
// smoothed over here.
func (d *DB) BuildArtifactLookup() (ArtifactLookup, error) {
	l := ArtifactLookup{Paths: map[string]bool{}, SkillNames: map[string]string{}}
	rows, err := d.sql.Query(`SELECT type, path, name FROM artifacts ORDER BY path`)
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

// ResolvedTouch is one usage signal resolved to the artifact it names,
// still carrying its originating tool_use's id - the identity
// ReplaceArtifactUsage dedupes on, same reasoning as ReplaceToolUsage.
type ResolvedTouch struct {
	ToolUseID    string
	ArtifactPath string
}

// Resolve turns raw ingest signals into ResolvedTouch rows ready for
// ReplaceArtifactUsage. A signal that does not match anything currently
// known (a skill invoked under a name Loom has not discovered, a file
// outside any known artifact's path) is dropped rather than guessed at -
// #39 answers "was a known artifact used", not "what files exist".
func (l ArtifactLookup) Resolve(skillTouches, fileTouches []ingest.ArtifactTouch) []ResolvedTouch {
	var out []ResolvedTouch
	for _, t := range skillTouches {
		if path, ok := l.SkillNames[t.Signal]; ok {
			out = append(out, ResolvedTouch{ToolUseID: t.ToolUseID, ArtifactPath: path})
		}
	}
	for _, t := range fileTouches {
		if l.Paths[t.Signal] {
			out = append(out, ResolvedTouch{ToolUseID: t.ToolUseID, ArtifactPath: t.Signal})
		}
	}
	return out
}

// ReplaceArtifactUsage writes runID's artifact_usage rows, one per touch,
// replacing whatever this run previously owned. Same dedup contract as
// ReplaceToolUsage: the DELETE covers only rows this run_id owns, so a
// re-ingest of a grown file rebuilds cleanly, and
// ON CONFLICT(tool_use_id) DO NOTHING is what stops a resumed session's
// replayed Skill/Read/Edit/Write calls from being recounted under a second
// run_id. Found missing by code review, the same gap ReplaceToolUsage had.
func (d *DB) ReplaceArtifactUsage(runID int64, touches []ResolvedTouch) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM artifact_usage WHERE run_id = ?`, runID); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`
		INSERT INTO artifact_usage (tool_use_id, run_id, artifact_path)
		VALUES (?, ?, ?)
		ON CONFLICT(tool_use_id) DO NOTHING`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, t := range touches {
		if _, err := stmt.Exec(t.ToolUseID, runID, t.ArtifactPath); err != nil {
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
		SELECT au.artifact_path, COUNT(*),
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

// AgentTypeLastUsed returns the most recent run's own timestamp for every
// agent_type that has at least one run, so retirement can answer "was this
// agent invoked" the way docs/design.md's B7a section says it can:
// runs.agent_type already exists (B3a), so agent usage needs no new signal
// - it was never wired into the retirement check itself, a gap code review
// found. Keys are agent_type values, matching artifacts.name for
// type='agent' rows (discover.go names an agent artifact after its file,
// the same string .meta.json's agentType carries).
func (d *DB) AgentTypeLastUsed() (map[string]string, error) {
	rows, err := d.sql.Query(`
		SELECT agent_type, MAX(COALESCE(started_at, ended_at, ''))
		FROM runs
		WHERE kind = 'agent' AND agent_type != ''
		GROUP BY agent_type`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := map[string]string{}
	for rows.Next() {
		var agentType, lastUsed string
		if err := rows.Scan(&agentType, &lastUsed); err != nil {
			return nil, err
		}
		out[agentType] = lastUsed
	}
	return out, rows.Err()
}
