package ledger

import "testing"

func TestStatusOnEmptyLedger(t *testing.T) {
	db := openTestDB(t)
	s, err := db.Status()
	if err != nil {
		t.Fatal(err)
	}
	if s.Runs != 0 || s.Artifacts != 0 || s.Policies != 0 {
		t.Errorf("expected an empty status, got %+v", s)
	}
	// Empty strings, not a crash or a bogus date, on a ledger with no runs.
	if s.EarliestRun != "" || s.LatestRun != "" {
		t.Errorf("run window on an empty ledger = %q..%q, want empty", s.EarliestRun, s.LatestRun)
	}
}

func TestStatusCounts(t *testing.T) {
	db := openTestDB(t)
	if err := db.InsertRun(RunRecord{Path: "s.jsonl", Kind: "session", SizeBytes: 10}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertRun(RunRecord{Path: "a1.jsonl", Kind: "agent", AgentType: "Explore", SizeBytes: 20}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertRun(RunRecord{Path: "a2.jsonl", Kind: "agent", AgentType: "fork", SizeBytes: 30}); err != nil {
		t.Fatal(err)
	}
	// An agent run with no readable .meta.json: counted, but not an agent type.
	if err := db.InsertRun(RunRecord{Path: "a3.jsonl", Kind: "agent", SizeBytes: 40}); err != nil {
		t.Fatal(err)
	}

	s, err := db.Status()
	if err != nil {
		t.Fatal(err)
	}
	if s.Runs != 4 || s.SessionRuns != 1 || s.AgentRuns != 3 {
		t.Errorf("got runs=%d sessions=%d agents=%d, want 4/1/3", s.Runs, s.SessionRuns, s.AgentRuns)
	}
	if s.AgentTypes != 2 {
		t.Errorf("AgentTypes = %d, want 2 (the unattributed run is not a type)", s.AgentTypes)
	}
	if s.Unattributed != 1 {
		t.Errorf("Unattributed = %d, want 1", s.Unattributed)
	}
}

func TestKnownRunsReportsRecordedSizes(t *testing.T) {
	db := openTestDB(t)
	if err := db.InsertRun(RunRecord{Path: "a.jsonl", Kind: "session", SizeBytes: 1234}); err != nil {
		t.Fatal(err)
	}
	known, err := db.KnownRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(known) != 1 || known[0].Path != "a.jsonl" || known[0].SizeBytes != 1234 {
		t.Errorf("KnownRuns = %+v, want one row with size 1234", known)
	}
}

func TestStatusCountsLanes(t *testing.T) {
	db := openTestDB(t)
	for _, r := range []struct {
		path, lane string
	}{
		{"a.jsonl", "-u-work"},
		{"b.jsonl", "-u-work"},
		{"c.jsonl", "-u-personal"},
		{"d.jsonl", ""}, // outside the projects root: real run, no lane
	} {
		if err := db.InsertRun(RunRecord{Path: r.path, Kind: "session", Lane: r.lane}); err != nil {
			t.Fatal(err)
		}
	}

	s, err := db.Status()
	if err != nil {
		t.Fatal(err)
	}
	if s.Lanes != 2 {
		t.Errorf("Lanes = %d, want 2 (distinct attributed lanes only)", s.Lanes)
	}
	if s.UnattributedLanes != 1 {
		t.Errorf("UnattributedLanes = %d, want 1", s.UnattributedLanes)
	}
	// Unattributed runs are counted, never dropped: the run is real and so is
	// its cost, whatever the tool can or cannot say about where it came from.
	if s.Runs != 4 {
		t.Errorf("Runs = %d, want 4", s.Runs)
	}
}
