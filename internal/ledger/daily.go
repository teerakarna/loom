package ledger

import (
	"sort"

	"github.com/teerakarna/loom/internal/ingest"
)

// ReplaceDailyUsage writes runID's per-day usage, replacing whatever the run
// owned before, so a re-ingest of a grown file rebuilds its days cleanly and the
// days always sum to the runs row written in the same pass (issue #142).
func (d *DB) ReplaceDailyUsage(runID int64, days map[string]ingest.Usage) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM daily_usage WHERE run_id = ?`, runID); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`
		INSERT INTO daily_usage (run_id, day, input_tokens, output_tokens,
			cache_read_tokens, cache_creation_tokens, weighted_cost)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for day, u := range days {
		if _, err := stmt.Exec(runID, day, u.InputTokens, u.OutputTokens,
			u.CacheReadInputTokens, u.CacheCreationInputTokens, ingest.WeightedCost(u)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PeriodCost is one period's weighted cost, and how many distinct runs did work
// in it. Runs is counted per period, not summed from days: a run that spans
// several days of a week is one run in that week.
type PeriodCost struct {
	Period       string
	Runs         int
	WeightedCost float64
}

// Undated is weighted cost that cannot be put on a day, kept apart rather than
// spread or guessed (constraint 11). Runs are runs with a cost and no daily rows
// at all: ingested before daily_usage existed and whose transcript is gone, so
// they cannot be re-read. Cost also includes responses that had no timestamp.
type Undated struct {
	Runs         int
	WeightedCost float64
}

// CostByPeriod returns weighted cost grouped by period(day), where day is a UTC
// YYYY-MM-DD and period maps it to a label that sorts in time order (the day
// itself, an ISO week, a month). Oldest first, plus what could not be dated.
// lane narrows to one lane; "" is the whole ledger.
func (d *DB) CostByPeriod(lane string, period func(day string) string) ([]PeriodCost, Undated, error) {
	where, args := "", []any{}
	if lane != "" {
		where, args = " WHERE r.lane = ?", []any{lane}
	}

	rows, err := d.sql.Query(`
		SELECT du.run_id, du.day, du.weighted_cost
		FROM daily_usage du JOIN runs r ON r.id = du.run_id`+where, args...)
	if err != nil {
		return nil, Undated{}, err
	}
	defer func() { _ = rows.Close() }()

	type acc struct {
		runs map[int64]struct{}
		cost float64
	}
	byPeriod := map[string]*acc{}
	var und Undated
	for rows.Next() {
		var runID int64
		var day string
		var cost float64
		if err := rows.Scan(&runID, &day, &cost); err != nil {
			return nil, Undated{}, err
		}
		if day == "" {
			und.WeightedCost += cost
			continue
		}
		key := period(day)
		a := byPeriod[key]
		if a == nil {
			a = &acc{runs: map[int64]struct{}{}}
			byPeriod[key] = a
		}
		a.runs[runID] = struct{}{}
		a.cost += cost
	}
	if err := rows.Err(); err != nil {
		return nil, Undated{}, err
	}

	// Runs with a cost and no daily rows at all. A zero-cost run has nothing to
	// date, so it is not counted as undated.
	var n int
	var c float64
	cond := " WHERE r.weighted_cost > 0 AND NOT EXISTS (SELECT 1 FROM daily_usage du WHERE du.run_id = r.id)"
	if lane != "" {
		cond += " AND r.lane = ?"
	}
	if err := d.sql.QueryRow(`SELECT COUNT(*), COALESCE(SUM(r.weighted_cost), 0) FROM runs r`+cond, args...).
		Scan(&n, &c); err != nil {
		return nil, Undated{}, err
	}
	und.Runs += n
	und.WeightedCost += c

	out := make([]PeriodCost, 0, len(byPeriod))
	for k, a := range byPeriod {
		out = append(out, PeriodCost{Period: k, Runs: len(a.runs), WeightedCost: a.cost})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Period < out[j].Period })
	return out, und, nil
}
