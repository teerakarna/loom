package ledger

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/azva-co/loom/internal/ingest"
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
//
// 2 is the lane and kind corruption fixed alongside this bump, and it is a
// repair rather than a backfill - the first time this counter has been used for
// one. A report narrowed to a single project derived every lane from the walked
// root instead of the projects root, so transcripts were filed under a session
// UUID, or under nothing at all for the files sitting directly in that root;
// kindForPath matched "subagents" as a substring, so the same file could be read
// as an agent run or a session run depending on which root discovered it. Both
// wrote a plausible wrong value and then made it permanent: size matches and the
// version is current, so NeedsIngest vetoes every later read that would have
// corrected it. A bump is the only repair path the design offers - there is
// deliberately no --force (docs/design.md, "No reprocessing flag") - so without
// one, a ledger that took a narrowed report stays wrong for as long as it lives.
const CurrentFeatureVersion = 2

// FreshnessState is the three-way answer to "does this stored run need a
// re-read, and for which of the two reasons" - NeedsIngest only needs the
// yes/no half of this, but a caller reporting freshness (loom status,
// get_recommendation's freshness block) needs to tell "grown since" apart
// from "unchanged, but predates a loom upgrade", which is a different fact
// about the file and a false reason if conflated (issue #89).
type FreshnessState int

const (
	FreshnessCurrent FreshnessState = iota
	// FreshnessStale means the file's size has changed since it was read.
	FreshnessStale
	// FreshnessNeedsReread means the size is unchanged but the stored row
	// predates CurrentFeatureVersion.
	FreshnessNeedsReread
)

// ClassifyFreshness is the one predicate NeedsIngest and every freshness-
// reporting caller both key their answer on, so neither can drift from the
// other the way two independently hand-written copies of this same switch
// already had started to (issue #96, found by code review on both #89's
// and #86's PRs: cmd/loom/status.go's freshness() and
// internal/mcp/server.go's computeLedgerFreshness each re-implemented this
// inline). storedSize and size are the size a run was ingested at and its
// current size on disk; storedVersion is the feature_version the row was
// stamped with.
func ClassifyFreshness(storedSize, size, storedVersion int64) FreshnessState {
	switch {
	case storedSize != size:
		return FreshnessStale
	case storedVersion < CurrentFeatureVersion:
		return FreshnessNeedsReread
	default:
		return FreshnessCurrent
	}
}

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
			kind = excluded.kind, model = excluded.model,
			-- An empty lane carries no information, so it must not displace a
			-- known one. Lane is derived from the root of the invocation that read
			-- the file, and LaneFromPath returns "" for a file sitting directly in
			-- that root - so re-reading a transcript under a report narrowed to one
			-- project recomputed its lane as empty and wrote that over the real
			-- one. Nothing repaired it afterwards: the size matched and
			-- the feature version was current, so NeedsIngest vetoed every later
			-- read and the run stayed in UnattributedLanes for good. The reverse
			-- never needs expressing - a run does not move out of the projects
			-- root without its path changing, which makes it a different row.
			lane = COALESCE(NULLIF(excluded.lane, ''), runs.lane),
			-- Same argument as lane, and for a nearly identical reason: agent_type
			-- is not in the transcript at all. It is read from the .meta.json
			-- companion beside it, and ReadAgentMeta answers "not found" for a
			-- companion that is absent, unreadable, not valid JSON, or names an
			-- empty type - all normal conditions on a corpus another tool writes.
			-- So a transcript that grows after its companion has been cleaned up
			-- re-reads with an empty agent type, and a plain overwrite would file
			-- the run under no type for good, since the size then matches and the
			-- feature version is current. An empty value here means "could not
			-- read it", never "this run has no agent type".
			agent_type = COALESCE(NULLIF(excluded.agent_type, ''), runs.agent_type),
			effort = excluded.effort, started_at = excluded.started_at, ended_at = excluded.ended_at,
			input_tokens = excluded.input_tokens, output_tokens = excluded.output_tokens,
			cache_read_tokens = excluded.cache_read_tokens,
			cache_creation_tokens = excluded.cache_creation_tokens,
			weighted_cost = excluded.weighted_cost, tool_use_count = excluded.tool_use_count,
			denial_count = excluded.denial_count, feedback_count = excluded.feedback_count,
			-- Same argument as lane, one step further: these three are NULL
			-- unless some session read in the *same batch* reported figures for
			-- this agent, and a re-read of one agent transcript is a batch of
			-- one. So an ordinary incremental report writes NULL over a real
			-- reconciliation figure whenever the parent session file has not
			-- itself changed, and NeedsIngest then vetoes the read that would
			-- restore it. NULL here means "nothing reported yet", never "the
			-- earlier figure was withdrawn", so keeping the stored one is the
			-- only reading that matches what the column means.
			reported_subagent_tokens = COALESCE(excluded.reported_subagent_tokens, runs.reported_subagent_tokens),
			reported_tool_uses = COALESCE(excluded.reported_tool_uses, runs.reported_tool_uses),
			reported_duration_ms = COALESCE(excluded.reported_duration_ms, runs.reported_duration_ms),
			feature_version = excluded.feature_version`,
		r.Path, r.SizeBytes, r.SessionID, r.Kind, r.Model, r.Lane, r.AgentType, r.Effort, formatTime(r.StartedAt), formatTime(r.EndedAt),
		r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheCreationTokens,
		r.WeightedCost, r.ToolUseCount, r.DenialCount, r.FeedbackCount,
		r.ReportedSubagentTokens, r.ReportedToolUses, r.ReportedDurationMs, CurrentFeatureVersion,
	)
	return err
}

// InvalidateRun marks one run as needing a re-read on the next report, by
// clearing the feature version NeedsIngest compares against. A missing row is
// not an error: there is then nothing to re-read and nothing stale to correct.
//
// It exists because a run is written in two steps that are not one
// transaction - InsertRun, then recordOccupancyAndUsage for the child rows -
// and the second one's failure is deliberately non-fatal, so one unreadable
// transcript cannot cost a whole report. The size and feature version are
// already stored by then, so NeedsIngest answers "no" from the next report
// onward and the child rows never arrive. Worse on the supersession path, where
// the old row's child rows have already been deleted to make room for them:
// there the loss is silent and total. Writing the version back down costs one
// UPDATE and makes the failure self-healing instead.
func (d *DB) InvalidateRun(path string) error {
	_, err := d.sql.Exec(`UPDATE runs SET feature_version = 0 WHERE path = ?`, path)
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
