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

// TestReportCostByKindSumsToTheHeadline is the guard for issue #9: a breakdown
// that does not add up to the total it explains is worse than none. It also
// pins the weights to internal/ingest's, so the two cannot drift apart.
func TestReportCostByKindSumsToTheHeadline(t *testing.T) {
	db := openTestDB(t)
	if err := db.InsertRun(RunRecord{
		Path: "a.jsonl", Kind: "session",
		InputTokens: 100, OutputTokens: 20, CacheReadTokens: 5000, CacheCreationTokens: 40,
		WeightedCost: 100*1.0 + 20*5.0 + 5000*0.1 + 40*1.25,
	}); err != nil {
		t.Fatal(err)
	}

	s, err := db.Report()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.CostByKind) != 4 {
		t.Fatalf("CostByKind = %d entries, want 4", len(s.CostByKind))
	}

	var sum, shares float64
	byKind := map[string]KindCost{}
	for _, k := range s.CostByKind {
		sum += k.Cost
		shares += k.Share
		byKind[k.Kind] = k
	}
	if sum != s.TotalWeightedCost {
		t.Errorf("kinds sum to %v but the headline is %v", sum, s.TotalWeightedCost)
	}
	if shares < 0.999 || shares > 1.001 {
		t.Errorf("shares sum to %v, want 1.0", shares)
	}
	// Cache reads dominate token counts but not cost, which is the whole point
	// of showing the split.
	if byKind["cache read"].Tokens != 5000 {
		t.Errorf("cache read tokens = %d, want 5000", byKind["cache read"].Tokens)
	}
	if byKind["cache read"].Cost != 500 {
		t.Errorf("cache read cost = %v, want 500 (5000 x 0.1)", byKind["cache read"].Cost)
	}
}

func TestReportCostByKindOnEmptyLedger(t *testing.T) {
	db := openTestDB(t)
	s, err := db.Report()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range s.CostByKind {
		if k.Share != 0 || k.Cost != 0 {
			t.Errorf("empty ledger produced %+v, want zeroes and no division by zero", k)
		}
	}
}

func TestReportByLaneAndFiltering(t *testing.T) {
	db := openTestDB(t)
	seed := []struct {
		path, lane string
		cost       float64
	}{
		{"a.jsonl", "-u-work", 100},
		{"b.jsonl", "-u-work", 300},
		{"c.jsonl", "-u-personal", 50},
		{"d.jsonl", "", 7}, // unattributable: still a real run
	}
	for _, r := range seed {
		// Tokens and weighted cost must agree, as they do for real ingest
		// where the cost is derived from the tokens. Input is weighted 1.0, so
		// input == cost keeps the fixture internally consistent and lets the
		// cost-by-kind assertion below mean something.
		if err := db.InsertRun(RunRecord{
			Path: r.path, Kind: "session", Lane: r.lane,
			InputTokens: int64(r.cost), WeightedCost: r.cost,
		}); err != nil {
			t.Fatal(err)
		}
	}

	all, err := db.Report()
	if err != nil {
		t.Fatal(err)
	}
	if all.TotalRuns != 4 || all.TotalWeightedCost != 457 {
		t.Errorf("whole ledger = %d runs / %v, want 4 / 457", all.TotalRuns, all.TotalWeightedCost)
	}
	if len(all.ByLane) != 3 {
		t.Errorf("ByLane = %d groups, want 3 (including the unattributed one)", len(all.ByLane))
	}

	work, err := db.ReportForLane("-u-work")
	if err != nil {
		t.Fatal(err)
	}
	if work.TotalRuns != 2 || work.TotalWeightedCost != 400 {
		t.Errorf("work lane = %d runs / %v, want 2 / 400", work.TotalRuns, work.TotalWeightedCost)
	}
	// The filter must reach every part of the summary, not just the headline.
	var kindSum float64
	for _, k := range work.CostByKind {
		kindSum += k.Cost
	}
	if kindSum != work.TotalWeightedCost {
		t.Errorf("filtered cost-by-kind sums to %v but the filtered headline is %v", kindSum, work.TotalWeightedCost)
	}
	if len(work.TopRuns) != 2 {
		t.Errorf("filtered TopRuns = %d, want 2", len(work.TopRuns))
	}

	// An empty lane means "unattributed", never "everything" - which is why
	// Report and ReportForLane are separate entry points.
	none, err := db.ReportForLane("nope")
	if err != nil {
		t.Fatal(err)
	}
	if none.TotalRuns != 0 {
		t.Errorf("unknown lane = %d runs, want 0", none.TotalRuns)
	}
}
