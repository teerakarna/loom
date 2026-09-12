package ledger

import "time"

// RunRecord is what gets written to the runs table for one ingested
// transcript file. ReportedSubagentTokens/ReportedToolUses/ReportedDurationMs
// are pointers so "not an agent run" and "agent run, but no notification
// found yet" are both representable as NULL rather than a misleading zero.
type RunRecord struct {
	Path                   string
	SessionID              string
	Kind                   string
	Model                  string
	StartedAt              time.Time
	EndedAt                time.Time
	InputTokens            int64
	OutputTokens           int64
	CacheReadTokens        int64
	CacheCreationTokens    int64
	WeightedCost           float64
	ToolUseCount           int
	DenialCount            int
	FeedbackCount          int
	ReportedSubagentTokens *int64
	ReportedToolUses       *int64
	ReportedDurationMs     *int64
}

// InsertRun writes one run. Fails on a duplicate path (UNIQUE constraint) —
// callers should check HasRun first if re-running ingest idempotently.
func (d *DB) InsertRun(r RunRecord) error {
	_, err := d.sql.Exec(`
		INSERT INTO runs (
			path, session_id, kind, model, started_at, ended_at,
			input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
			weighted_cost, tool_use_count, denial_count, feedback_count,
			reported_subagent_tokens, reported_tool_uses, reported_duration_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Path, r.SessionID, r.Kind, r.Model, formatTime(r.StartedAt), formatTime(r.EndedAt),
		r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheCreationTokens,
		r.WeightedCost, r.ToolUseCount, r.DenialCount, r.FeedbackCount,
		r.ReportedSubagentTokens, r.ReportedToolUses, r.ReportedDurationMs,
	)
	return err
}

func formatTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Format(time.RFC3339)
}

// Summary is the aggregate `loom report` reads back.
type Summary struct {
	TotalRuns          int
	SessionRuns        int
	AgentRuns          int
	TotalWeightedCost  float64
	TotalToolUses      int
	TotalDenials       int
	TotalFeedback      int
	ByModel            []ModelCost
	UnreconciledAgents int // agent runs with a reported figure that doesn't match a computed one
}

// ModelCost is one row of the by-model cost breakdown.
type ModelCost struct {
	Model        string
	Runs         int
	WeightedCost float64
}

// Report aggregates everything currently in the ledger. It does not attempt
// to reconcile weighted_cost against reported_subagent_tokens — see
// docs/transcript-schema.md, "Reconciliation does NOT hold under naive
// summing" — it only counts how many agent runs have a reported figure at
// all, as a visibility signal.
func (d *DB) Report() (Summary, error) {
	var s Summary
	row := d.sql.QueryRow(`
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN kind = 'session' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN kind = 'agent' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(weighted_cost), 0),
			COALESCE(SUM(tool_use_count), 0),
			COALESCE(SUM(denial_count), 0),
			COALESCE(SUM(feedback_count), 0),
			COALESCE(SUM(CASE WHEN kind = 'agent' AND reported_subagent_tokens IS NOT NULL THEN 1 ELSE 0 END), 0)
		FROM runs`)
	if err := row.Scan(&s.TotalRuns, &s.SessionRuns, &s.AgentRuns, &s.TotalWeightedCost,
		&s.TotalToolUses, &s.TotalDenials, &s.TotalFeedback, &s.UnreconciledAgents); err != nil {
		return s, err
	}

	rows, err := d.sql.Query(`
		SELECT COALESCE(model, '(unknown)'), COUNT(*), COALESCE(SUM(weighted_cost), 0)
		FROM runs GROUP BY model ORDER BY SUM(weighted_cost) DESC`)
	if err != nil {
		return s, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var mc ModelCost
		if err := rows.Scan(&mc.Model, &mc.Runs, &mc.WeightedCost); err != nil {
			return s, err
		}
		s.ByModel = append(s.ByModel, mc)
	}
	return s, rows.Err()
}
