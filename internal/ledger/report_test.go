package ledger

import (
	"fmt"
	"testing"
)

// seedRuns inserts n runs with the given costs, alternating kinds so both
// groupings have something to work with.
func seedRuns(t *testing.T, db *DB, costs ...float64) {
	t.Helper()
	for i, c := range costs {
		rec := RunRecord{
			Path:         fmt.Sprintf("run-%d.jsonl", i),
			Kind:         "session",
			Model:        "claude-sonnet-5",
			WeightedCost: c,
		}
		if i%2 == 1 {
			rec.Kind = "agent"
			rec.AgentType = "Explore"
		}
		if err := db.InsertRun(rec); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReportPerRunCost(t *testing.T) {
	db := openTestDB(t)
	// Two models: one expensive but rare, one cheap but frequent. Totals alone
	// would rank them the other way round, which is the bug issue #10 is about.
	for i := range 1 {
		if err := db.InsertRun(RunRecord{Path: fmt.Sprintf("big-%d.jsonl", i), Kind: "session", Model: "expensive", WeightedCost: 100}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 10 {
		if err := db.InsertRun(RunRecord{Path: fmt.Sprintf("small-%d.jsonl", i), Kind: "session", Model: "cheap", WeightedCost: 20}); err != nil {
			t.Fatal(err)
		}
	}

	s, err := db.Report()
	if err != nil {
		t.Fatal(err)
	}

	byModel := map[string]ModelCost{}
	for _, m := range s.ByModel {
		byModel[m.Model] = m
	}
	// By total, "cheap" leads (200 vs 100). By per-run, "expensive" leads
	// (100 vs 20). Both must be reported, or the reader draws the wrong
	// conclusion from whichever one is shown.
	if byModel["cheap"].WeightedCost <= byModel["expensive"].WeightedCost {
		t.Errorf("expected cheap to lead on total: %+v", byModel)
	}
	if byModel["expensive"].PerRun <= byModel["cheap"].PerRun {
		t.Errorf("expected expensive to lead per run: got %v vs %v", byModel["expensive"].PerRun, byModel["cheap"].PerRun)
	}
	if byModel["expensive"].PerRun != 100 {
		t.Errorf("PerRun = %v, want 100", byModel["expensive"].PerRun)
	}
}

func TestReportConcentration(t *testing.T) {
	db := openTestDB(t)
	// One run dominates: 900 of 1000 total.
	seedRuns(t, db, 900, 40, 30, 20, 10)

	s, err := db.Report()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Concentration) == 0 {
		t.Fatal("expected concentration points")
	}
	if s.Concentration[0].N != 1 {
		t.Errorf("first concentration point N = %d, want 1", s.Concentration[0].N)
	}
	if got := s.Concentration[0].Share; got < 0.89 || got > 0.91 {
		t.Errorf("top-1 share = %v, want ~0.90", got)
	}
}

func TestReportTopRunsAndAgentTypes(t *testing.T) {
	db := openTestDB(t)
	seedRuns(t, db, 500, 300, 100, 50, 25, 15, 10)

	s, err := db.Report()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.TopRuns) != topRunsShown {
		t.Errorf("TopRuns = %d, want %d (bounded output, constraint 10)", len(s.TopRuns), topRunsShown)
	}
	// Descending, and the largest carries its share of the total.
	if s.TopRuns[0].WeightedCost != 500 {
		t.Errorf("TopRuns[0].WeightedCost = %v, want 500", s.TopRuns[0].WeightedCost)
	}
	if s.TopRuns[0].Share <= s.TopRuns[1].Share {
		t.Error("top runs must be ordered by cost descending")
	}
	if len(s.ByAgentType) == 0 {
		t.Error("expected an agent-type breakdown")
	}
	for _, a := range s.ByAgentType {
		if a.Runs > 0 && a.PerRun == 0 {
			t.Errorf("agent type %q has runs but no per-run figure", a.AgentType)
		}
	}
}

func TestReportEmptyLedgerHasNoDivisionByZero(t *testing.T) {
	db := openTestDB(t)
	s, err := db.Report()
	if err != nil {
		t.Fatal(err)
	}
	if s.TotalRuns != 0 || len(s.TopRuns) != 0 || len(s.Concentration) != 0 {
		t.Errorf("expected an empty summary, got %+v", s)
	}
}
