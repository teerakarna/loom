package ledger

import (
	"testing"
	"time"

	"github.com/teerakarna/loom/internal/ingest"
)

// TestCostByPeriodCountsRunsPerPeriod pins issue #142's grouping: a run that
// works across several days of one week is one run in that week, not one per
// day, and a run with a cost but no daily rows is reported as undated rather
// than dropped or given a day.
func TestCostByPeriodCountsRunsPerPeriod(t *testing.T) {
	db := openTestDB(t)
	for _, p := range []string{"/a.jsonl", "/b.jsonl"} {
		if err := db.InsertRun(RunRecord{Path: p, Kind: "session", WeightedCost: 100}); err != nil {
			t.Fatal(err)
		}
	}
	// No daily rows (transcript gone): one spans days and stays undated, one
	// sits inside a single UTC day and is dated by it exactly.
	mustInsert := func(path string, start, end time.Time, cost float64) {
		if err := db.InsertRun(RunRecord{Path: path, Kind: "session", StartedAt: start, EndedAt: end, WeightedCost: cost}); err != nil {
			t.Fatal(err)
		}
	}
	mustInsert("/old-long.jsonl", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC), 100)
	mustInsert("/old-short.jsonl", time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC), time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC), 7)
	idA, _ := db.RunIDByPath("/a.jsonl")
	idB, _ := db.RunIDByPath("/b.jsonl")
	// a spans two days of the same month; b is one day; old has no days at all.
	if err := db.ReplaceDailyUsage(idA, map[string]ingest.Usage{
		"2026-11-02": {InputTokens: 10}, "2026-11-03": {InputTokens: 20},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceDailyUsage(idB, map[string]ingest.Usage{"2026-11-03": {InputTokens: 5}}); err != nil {
		t.Fatal(err)
	}

	month := func(d string) string { return d[:7] }
	periods, undated, err := db.CostByPeriod("", month)
	if err != nil {
		t.Fatal(err)
	}
	if len(periods) != 1 || periods[0].Period != "2026-11" {
		t.Fatalf("periods = %+v, want one period 2026-11", periods)
	}
	if periods[0].Runs != 3 {
		t.Errorf("runs in 2026-11 = %d, want 3 (a's two days are one run, plus b and old-short)", periods[0].Runs)
	}
	if periods[0].WeightedCost != 42 {
		t.Errorf("weighted cost = %v, want 42 (35 from daily rows + 7 from the single-day run)", periods[0].WeightedCost)
	}
	if undated.Runs != 1 || undated.WeightedCost != 100 {
		t.Errorf("undated = %+v, want only the multi-day run with no days, cost 100", undated)
	}

	// Rewriting a run's days replaces them, it does not add to them.
	if err := db.ReplaceDailyUsage(idB, map[string]ingest.Usage{"2026-11-04": {InputTokens: 1}}); err != nil {
		t.Fatal(err)
	}
	day := func(d string) string { return d }
	periods, _, _ = db.CostByPeriod("", day)
	got := map[string]float64{}
	for _, p := range periods {
		got[p.Period] = p.WeightedCost
	}
	if got["2026-11-03"] != 27 || got["2026-11-04"] != 1 {
		t.Errorf("after rewrite: %+v, want 2026-11-03=27 (a's 20 + old-short's 7) and 2026-11-04=1", got)
	}
}
