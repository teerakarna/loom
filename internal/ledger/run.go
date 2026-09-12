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
	AgentType              string // "" for session runs, and for agents with no readable .meta.json
	Effort                 string // "" when the transcript carried no effort
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
			path, session_id, kind, model, agent_type, effort, started_at, ended_at,
			input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
			weighted_cost, tool_use_count, denial_count, feedback_count,
			reported_subagent_tokens, reported_tool_uses, reported_duration_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Path, r.SessionID, r.Kind, r.Model, r.AgentType, r.Effort, formatTime(r.StartedAt), formatTime(r.EndedAt),
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
	ByAgentType        []AgentTypeCost
	TopRuns            []RunCost // most expensive runs, descending
	Concentration      []ShareAtN
	UnreconciledAgents int // agent runs with a reported figure that doesn't match a computed one
}

// ModelCost is one row of the by-model cost breakdown. PerRun is the figure
// that matters for a routing decision: totals conflate "expensive per run"
// with "used more often", and comparing models on totals alone can invert the
// real ordering when run counts differ (see issue #10).
type ModelCost struct {
	Model        string
	Runs         int
	WeightedCost float64
	PerRun       float64
}

// AgentTypeCost is the same breakdown by agent type, available since B3a
// started recording it. This is the grouping model pinning actually keys on.
type AgentTypeCost struct {
	AgentType    string // "" rendered as "(unattributed)" by the caller
	Runs         int
	WeightedCost float64
	PerRun       float64
}

// RunCost is one run in the most-expensive listing. Without this, a single
// dominant run is invisible in any grouped view, and its cost is silently
// attributed to whatever model or agent type it happened to use.
type RunCost struct {
	Path         string
	Kind         string
	Model        string
	AgentType    string
	WeightedCost float64
	Share        float64 // fraction of the total, 0-1
}

// ShareAtN is "the top N runs account for this share of total cost". A small
// number of runs accounting for nearly everything is the normal case, and it
// changes what a reader should do about it: tune the outliers, not the
// average.
type ShareAtN struct {
	N     int
	Share float64 // 0-1
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

	if err := d.scanGrouped(&s); err != nil {
		return s, err
	}
	if err := d.scanTopRuns(&s); err != nil {
		return s, err
	}
	return s, nil
}

// scanGrouped fills the by-model and by-agent-type breakdowns.
func (d *DB) scanGrouped(s *Summary) error {
	rows, err := d.sql.Query(`
		SELECT COALESCE(model, '(unknown)'), COUNT(*), COALESCE(SUM(weighted_cost), 0)
		FROM runs GROUP BY model ORDER BY SUM(weighted_cost) DESC`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var mc ModelCost
		if err := rows.Scan(&mc.Model, &mc.Runs, &mc.WeightedCost); err != nil {
			return err
		}
		mc.PerRun = perRun(mc.WeightedCost, mc.Runs)
		s.ByModel = append(s.ByModel, mc)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	agentRows, err := d.sql.Query(`
		SELECT agent_type, COUNT(*), COALESCE(SUM(weighted_cost), 0)
		FROM runs WHERE kind = 'agent' GROUP BY agent_type ORDER BY SUM(weighted_cost) DESC`)
	if err != nil {
		return err
	}
	defer func() { _ = agentRows.Close() }()
	for agentRows.Next() {
		var ac AgentTypeCost
		if err := agentRows.Scan(&ac.AgentType, &ac.Runs, &ac.WeightedCost); err != nil {
			return err
		}
		ac.PerRun = perRun(ac.WeightedCost, ac.Runs)
		s.ByAgentType = append(s.ByAgentType, ac)
	}
	return agentRows.Err()
}

// topRunsShown bounds the most-expensive listing. Constraint 10: a report is
// output, and output needs a limit stated where it is produced. Five is enough
// to see a dominant run without turning a summary into a log.
const topRunsShown = 5

// concentrationPoints are the cut-offs reported for "top N runs account for
// X% of cost".
var concentrationPoints = []int{1, 3, 5}

func (d *DB) scanTopRuns(s *Summary) error {
	rows, err := d.sql.Query(`
		SELECT path, kind, COALESCE(model, '(unknown)'), agent_type, weighted_cost
		FROM runs ORDER BY weighted_cost DESC`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	var running float64
	n := 0
	next := 0
	for rows.Next() {
		var rc RunCost
		if err := rows.Scan(&rc.Path, &rc.Kind, &rc.Model, &rc.AgentType, &rc.WeightedCost); err != nil {
			return err
		}
		n++
		running += rc.WeightedCost
		if s.TotalWeightedCost > 0 {
			rc.Share = rc.WeightedCost / s.TotalWeightedCost
		}
		if len(s.TopRuns) < topRunsShown {
			s.TopRuns = append(s.TopRuns, rc)
		}
		for next < len(concentrationPoints) && concentrationPoints[next] == n {
			share := 0.0
			if s.TotalWeightedCost > 0 {
				share = running / s.TotalWeightedCost
			}
			s.Concentration = append(s.Concentration, ShareAtN{N: n, Share: share})
			next++
		}
	}
	return rows.Err()
}

// perRun guards against a zero divisor; a group with no runs has no per-run
// cost rather than an infinity.
func perRun(total float64, runs int) float64 {
	if runs == 0 {
		return 0
	}
	return total / float64(runs)
}
