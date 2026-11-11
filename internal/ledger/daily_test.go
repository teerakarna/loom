package ledger

import (
	"testing"

	"github.com/teerakarna/loom/internal/ingest"
)

// TestCostByPeriodCountsRunsPerPeriod pins issue #142's grouping: a run that
// works across several days of one week is one run in that week, not one per
// day, and a run with a cost but no daily rows is reported as undated rather
// than dropped or given a day.
func TestCostByPeriodCountsRunsPerPeriod(t *testing.T) {
	db := openTestDB(t)
	for _, p := range []string{"/a.jsonl", "/b.jsonl", "/old.jsonl"} {
		if err := db.InsertRun(RunRecord{Path: p, Kind: "session", WeightedCost: 100}); err != nil {
			t.Fatal(err)
		}
	}
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
	if periods[0].Runs != 2 {
		t.Errorf("runs in 2026-11 = %d, want 2 (a's two days are one run)", periods[0].Runs)
	}
	if periods[0].WeightedCost != 35 {
		t.Errorf("weighted cost = %v, want 35", periods[0].WeightedCost)
	}
	if undated.Runs != 1 || undated.WeightedCost != 100 {
		t.Errorf("undated = %+v, want the one run with no days, cost 100", undated)
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
	if got["2026-11-03"] != 20 || got["2026-11-04"] != 1 {
		t.Errorf("after rewrite: %+v, want 2026-11-03=20 (a only) and 2026-11-04=1", got)
	}
}
