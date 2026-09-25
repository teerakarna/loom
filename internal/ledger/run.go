package ledger

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/teerakarna/loom/internal/ingest"
)

// CurrentFeatureVersion is the schema/feature version InsertRun stamps onto
// every run it writes. Bump it whenever a new derived table or column needs
// data backfilled onto files that were ingested before that feature existed
// (issue #49) - NeedsIngest re-reads any run stored below this value
// regardless of whether its size has changed, so the backfill happens on the
// next `loom report` with nothing for the user to know or ask for.
//
// 1 is B7b/B7a's occupancy and asset-usage tables (tool_usage, compactions,
// asset_usage), the gap this mechanism was built to close: a session file
// untouched since before those tables existed had a runs row but none of
// their data, forever, confirmed on this machine's own real ledger.
const CurrentFeatureVersion = 1

// RunRecord is what gets written to the runs table for one ingested
// transcript file. ReportedSubagentTokens/ReportedToolUses/ReportedDurationMs
// are pointers so "not an agent run" and "agent run, but no notification
// found yet" are both representable as NULL rather than a misleading zero.
type RunRecord struct {
	Path                   string
	SizeBytes              int64 // file size this run was ingested from; drives re-ingest
	SessionID              string
	Kind                   string
	Model                  string
	Lane                   string // project directory the session ran in; "" if unattributable
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

// InsertRun writes one run, replacing any existing row for the same path. A
// transcript that has grown since it was last ingested is re-read in full and
// its row replaced, rather than accumulating a second row for the same file
// (see NeedsIngest for why re-reading is necessary at all). "One file, one
// run" stays true.
func (d *DB) InsertRun(r RunRecord) error {
	_, err := d.sql.Exec(`
		INSERT INTO runs (
			path, size_bytes, session_id, kind, model, lane, agent_type, effort, started_at, ended_at,
			input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
			weighted_cost, tool_use_count, denial_count, feedback_count,
			reported_subagent_tokens, reported_tool_uses, reported_duration_ms, feature_version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			size_bytes = excluded.size_bytes, session_id = excluded.session_id,
			kind = excluded.kind, model = excluded.model, lane = excluded.lane,
			agent_type = excluded.agent_type,
			effort = excluded.effort, started_at = excluded.started_at, ended_at = excluded.ended_at,
			input_tokens = excluded.input_tokens, output_tokens = excluded.output_tokens,
			cache_read_tokens = excluded.cache_read_tokens,
			cache_creation_tokens = excluded.cache_creation_tokens,
			weighted_cost = excluded.weighted_cost, tool_use_count = excluded.tool_use_count,
			denial_count = excluded.denial_count, feedback_count = excluded.feedback_count,
			reported_subagent_tokens = excluded.reported_subagent_tokens,
			reported_tool_uses = excluded.reported_tool_uses,
			reported_duration_ms = excluded.reported_duration_ms,
			feature_version = excluded.feature_version`,
		r.Path, r.SizeBytes, r.SessionID, r.Kind, r.Model, r.Lane, r.AgentType, r.Effort, formatTime(r.StartedAt), formatTime(r.EndedAt),
		r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheCreationTokens,
		r.WeightedCost, r.ToolUseCount, r.DenialCount, r.FeedbackCount,
		r.ReportedSubagentTokens, r.ReportedToolUses, r.ReportedDurationMs, CurrentFeatureVersion,
	)
	return err
}

// formatTime renders a timestamp for storage, always in UTC.
//
// The UTC conversion is load-bearing, not tidiness. SQLite compares these as
// strings, and RFC3339 is only lexically ordered when every value shares an
// offset. Storing local time gave "2026-09-13T18:04:26+07:00" for a policy and
// "2026-09-13T12:04:32Z" for a run that happened an hour later - and the run
// sorted *earlier*, because "12" < "18" as text. Every comparison against a
// stored time was silently wrong whenever the machine was not on UTC.
//
// Found by running loop closure for real: the tests all passed because they
// construct times with time.UTC, so both sides matched by accident. Only real
// usage mixes a local clock with transcript timestamps that are already Z.
func formatTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// parseTime is formatTime's counterpart, for the rare caller that needs a
// stored timestamp back as a time.Time rather than as an opaque string to
// compare or display (issue #76: propose.needsProtection measures how long
// a streak has run, which needs real duration arithmetic, not just
// ordering). An empty string (formatTime's NULL) reports the zero time and
// no error - the caller's job to treat that as "not set", not as a parse
// failure.
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, s)
}

// Summary is the aggregate `loom report` reads back.
type Summary struct {
	TotalRuns         int
	SessionRuns       int
	AgentRuns         int
	TotalWeightedCost float64
	// CostByKind is the weighted contribution of each token class to
	// TotalWeightedCost. Without it the headline is a bare nine-digit number
	// with no reference point, and it conceals the single most useful fact
	// about a corpus: whether the spend is dominated by cheap cached input or
	// by expensive fresh output. Those imply opposite actions (issue #9).
	CostByKind         []KindCost
	TotalToolUses      int
	TotalDenials       int
	TotalFeedback      int
	ByModel            []ModelCost
	ByAgentType        []AgentTypeCost
	ByLane             []LaneCost
	TopRuns            []RunCost // most expensive runs, descending
	Concentration      []ShareAtN
	UnreconciledAgents int // agent runs with a reported figure that doesn't match a computed one
}

// KindCost is one token class's contribution to the weighted total.
type KindCost struct {
	Kind   string
	Cost   float64
	Share  float64 // fraction of the weighted total, 0-1
	Tokens int64
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

// LaneCost is cost grouped by the project directory a session ran in. This is
// B4's whole output: it needs no manifest because the lane is already in the
// transcript path (docs/design.md, "Filtering, not a manifest").
type LaneCost struct {
	Lane         string // "" means unattributable, rendered as such rather than hidden
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
// to reconcile weighted_cost against reported_subagent_tokens - see
// docs/transcript-schema.md, "Reconciliation does NOT hold under naive
// summing" - it only counts how many agent runs have a reported figure at
// all, as a visibility signal.
// Report aggregates the whole ledger. ReportForLane narrows the same summary
// to one lane.
func (d *DB) Report() (Summary, error) { return d.report("") }

// ReportForLane is Report restricted to runs from one lane. An empty lane
// string would mean "unattributed" rather than "everything", so the two
// entry points are separate: a filter that silently means its own opposite
// when passed a zero value is the kind of thing that bites once and is never
// trusted again.
func (d *DB) ReportForLane(lane string) (Summary, error) { return d.report(lane) }

func (d *DB) report(lane string) (Summary, error) {
	var s Summary
	// One predicate threaded through every query, so a filtered report and a
	// whole-ledger report can never diverge in what they count.
	where, args := "", []any(nil)
	if lane != "" {
		where, args = " WHERE lane = ?", []any{lane}
	}
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
		FROM runs`+where, args...)
	if err := row.Scan(&s.TotalRuns, &s.SessionRuns, &s.AgentRuns, &s.TotalWeightedCost,
		&s.TotalToolUses, &s.TotalDenials, &s.TotalFeedback, &s.UnreconciledAgents); err != nil {
		return s, err
	}

	if err := d.scanCostByKind(&s, where, args); err != nil {
		return s, err
	}
	if err := d.scanGrouped(&s, where, args); err != nil {
		return s, err
	}
	if err := d.scanTopRuns(&s, where, args); err != nil {
		return s, err
	}
	return s, nil
}

// scanCostByKind splits the weighted total by token class, using the same
// weights internal/ingest applies, so the two can never silently disagree
// about what a token costs.
func (d *DB) scanCostByKind(s *Summary, where string, args []any) error {
	row := d.sql.QueryRow(`
		SELECT COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		       COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_creation_tokens),0)
		FROM runs`+where, args...)
	var in, out, cr, cw int64
	if err := row.Scan(&in, &out, &cr, &cw); err != nil {
		return err
	}
	for _, k := range []struct {
		name   string
		tokens int64
		weight float64
	}{
		{"input", in, ingest.WeightInput},
		{"output", out, ingest.WeightOutput},
		{"cache read", cr, ingest.WeightCacheRead},
		{"cache write", cw, ingest.WeightCacheWrite},
	} {
		cost := float64(k.tokens) * k.weight
		share := 0.0
		if s.TotalWeightedCost > 0 {
			share = cost / s.TotalWeightedCost
		}
		s.CostByKind = append(s.CostByKind, KindCost{Kind: k.name, Cost: cost, Share: share, Tokens: k.tokens})
	}
	return nil
}

// scanGrouped fills the by-model and by-agent-type breakdowns.
func (d *DB) scanGrouped(s *Summary, where string, args []any) error {
	rows, err := d.sql.Query(`
		SELECT COALESCE(model, '(unknown)'), COUNT(*), COALESCE(SUM(weighted_cost), 0)
		FROM runs`+where+` GROUP BY model ORDER BY SUM(weighted_cost) DESC`, args...)
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

	laneRows, err := d.sql.Query(`
		SELECT lane, COUNT(*), COALESCE(SUM(weighted_cost), 0)
		FROM runs`+where+` GROUP BY lane ORDER BY SUM(weighted_cost) DESC`, args...)
	if err != nil {
		return err
	}
	defer func() { _ = laneRows.Close() }()
	for laneRows.Next() {
		var lc LaneCost
		if err := laneRows.Scan(&lc.Lane, &lc.Runs, &lc.WeightedCost); err != nil {
			return err
		}
		lc.PerRun = perRun(lc.WeightedCost, lc.Runs)
		s.ByLane = append(s.ByLane, lc)
	}
	if err := laneRows.Err(); err != nil {
		return err
	}

	agentRows, err := d.sql.Query(`
		SELECT agent_type, COUNT(*), COALESCE(SUM(weighted_cost), 0)
		FROM runs WHERE kind = 'agent'`+strings.Replace(where, " WHERE ", " AND ", 1)+` GROUP BY agent_type ORDER BY SUM(weighted_cost) DESC`, args...)
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

func (d *DB) scanTopRuns(s *Summary, where string, args []any) error {
	rows, err := d.sql.Query(`
		SELECT path, kind, COALESCE(model, '(unknown)'), agent_type, weighted_cost
		FROM runs`+where+` ORDER BY weighted_cost DESC`, args...)
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

// DeleteRuns removes the named runs and everything hanging off them,
// returning the paths it actually deleted. A path with no row is skipped
// rather than reported as an error, so the return value is the only honest
// basis for telling a user what went: it can be shorter than the input, and a
// caller that echoes its own candidate list instead claims deletions that did
// not happen.
//
// Why this exists at all: the ingester once treated every .jsonl under the
// projects root as a transcript, so a workflow's orchestration journal became
// a run with no model, no tokens and no tool calls. Fixing discovery
// (ingest.IsTranscript) stops new ones, but it cannot help a ledger that
// already holds some - and it makes them worse, because they are now in the
// ledger and never returned by Walk, which `loom status` reads as "in ledger,
// gone from disk" for a file that is sitting right there. So the prune is
// part of the fix, not a follow-up.
//
// The child rows are deleted explicitly. There is no ON DELETE CASCADE and
// foreign keys are not enabled on this connection, so a bare delete from
// `runs` would leave tool_usage, compactions and asset_usage rows pointing at
// an id that no longer exists. `runs.id` is AUTOINCREMENT, so a later run
// never inherits those rows by reusing the id - the hazard is the other
// direction: tool_usage.tool_use_id is a global primary key and
// compactions.boundary_uuid globally unique, both first-seen-wins via ON
// CONFLICT DO NOTHING. A leftover child row keeps a replayed event's id
// claimed by a dead run, so when a resumed session replays that event its
// copy is silently dropped and the inner join in occupancy() never sees it.
//
// Which is also why this is not a general-purpose "delete any run" tool. A
// session transcript owns the ids its resumed continuation replays, so
// deleting one drops those events from the continuation's occupancy, and the
// only way back is re-ingesting the deleted file.
func (d *DB) DeleteRuns(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var deleted []string
	for _, p := range paths {
		var id int64
		if err := tx.QueryRow(`SELECT id FROM runs WHERE path = ?`, p).Scan(&id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return nil, err
		}
		for _, table := range []string{"tool_usage", "compactions", "asset_usage"} {
			if _, err := tx.Exec(`DELETE FROM `+table+` WHERE run_id = ?`, id); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(`DELETE FROM runs WHERE id = ?`, id); err != nil {
			return nil, err
		}
		deleted = append(deleted, p)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return deleted, nil
}
