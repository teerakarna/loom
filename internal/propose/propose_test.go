package propose

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/teerakarna/loom/internal/ledger"
	"github.com/teerakarna/loom/internal/policy"
)

func openDB(t *testing.T) *ledger.DB {
	t.Helper()
	db, err := ledger.Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

var now = time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)

func TestRetireOnlyWhenGenuinelyStale(t *testing.T) {
	db := openDB(t)
	// Seen yesterday: not a candidate. A skill used twice a year is not dead.
	if err := db.UpsertArtifact(ledger.ArtifactRecord{Kind: "skill", Path: "/s/fresh.md", Name: "fresh"}, now.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Unseen well past the threshold.
	if err := db.UpsertArtifact(ledger.ArtifactRecord{Kind: "skill", Path: "/s/old.md", Name: "old"}, now.Add(-StaleAfter-48*time.Hour)); err != nil {
		t.Fatal(err)
	}

	ps, err := Generate(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 {
		t.Fatalf("got %d proposals, want 1: %+v", len(ps), ps)
	}
	if ps[0].Kind != KindRetireArtifact || ps[0].Subject != "/s/old.md" {
		t.Errorf("got %+v", ps[0])
	}
	if !TouchesUserFiles(ps[0].Kind) {
		t.Error("a retire proposal touches user files and must be flagged as such")
	}
}

func seedAgentRuns(t *testing.T, db *ledger.DB, agentType string, n int, denials int) {
	t.Helper()
	for i := range n {
		d := 0
		if i < denials {
			d = 1
		}
		if err := db.InsertRun(ledger.RunRecord{
			Path: fmt.Sprintf("/a/%s-%d.jsonl", agentType, i), Kind: "agent",
			AgentType: agentType, Model: "claude-haiku-4-5",
			WeightedCost: 100, ToolUseCount: 3, DenialCount: d,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// The load-bearing test for constraint 11: thin evidence must not become a
// recommendation.
func TestPinModelRespectsMinSampleSize(t *testing.T) {
	db := openDB(t)
	seedAgentRuns(t, db, "Explore", policy.MinSampleSize-1, 0)

	ps, err := Generate(db, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.Kind == KindPinModel {
			t.Fatalf("proposed a pin on %d runs, below the %d threshold", policy.MinSampleSize-1, policy.MinSampleSize)
		}
	}

	// One more run crosses it.
	seedAgentRuns2 := func() {
		if err := db.InsertRun(ledger.RunRecord{
			Path: "/a/Explore-extra.jsonl", Kind: "agent", AgentType: "Explore",
			Model: "claude-haiku-4-5", WeightedCost: 100, ToolUseCount: 3,
		}); err != nil {
			t.Fatal(err)
		}
	}
	seedAgentRuns2()

	ps, err = Generate(db, now)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range ps {
		if p.Kind == KindPinModel && p.Subject == "Explore" {
			found = true
			if p.SampleSize != policy.MinSampleSize {
				t.Errorf("SampleSize = %d, want %d", p.SampleSize, policy.MinSampleSize)
			}
			if TouchesUserFiles(p.Kind) {
				t.Error("a pin touches only loom's own state")
			}
		}
	}
	if !found {
		t.Error("expected a pin proposal once the threshold is met")
	}
}

// The design doc's own guardrail: never propose a model change for an agent
// type whose rework rate is already elevated. The problem there is quality,
// and a model swap is the wrong lever.
func TestPinModelSkipsElevatedRework(t *testing.T) {
	db := openDB(t)
	seedAgentRuns(t, db, "Flaky", policy.MinSampleSize, policy.MinSampleSize)

	ps, err := Generate(db, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.Kind == KindPinModel {
			t.Error("proposed a pin despite an elevated denial rate")
		}
	}
}

func TestPinModelSkipsWhenAPolicyAlreadyExists(t *testing.T) {
	db := openDB(t)
	seedAgentRuns(t, db, "Explore", policy.MinSampleSize, 0)
	if err := db.UpsertPolicy(ledger.PolicyRow{
		CriteriaVersion: "v1", AgentType: "Explore", Model: "opus", Effort: "high", Source: "human",
	}, now); err != nil {
		t.Fatal(err)
	}

	ps, err := Generate(db, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.Kind == KindPinModel {
			t.Error("relitigated a deliberate policy the user already set")
		}
	}
}

// Dedupe is what makes this bearable: a dismissal must survive repeated runs,
// and must end when the facts change.
func TestDismissalHoldsUntilEvidenceChanges(t *testing.T) {
	db := openDB(t)
	if err := db.UpsertArtifact(ledger.ArtifactRecord{Kind: "skill", Path: "/s/old.md", Name: "old"}, now.Add(-StaleAfter-48*time.Hour)); err != nil {
		t.Fatal(err)
	}

	ps, _ := Generate(db, now)
	if _, err := Store(db, ps, now); err != nil {
		t.Fatal(err)
	}
	pending, _ := db.ListProposals(true)
	if len(pending) != 1 {
		t.Fatalf("got %d pending, want 1", len(pending))
	}
	if err := db.DismissProposal(pending[0].ID); err != nil {
		t.Fatal(err)
	}

	// Run again with nothing changed: must stay dismissed.
	ps, _ = Generate(db, now)
	if _, err := Store(db, ps, now); err != nil {
		t.Fatal(err)
	}
	if pending, _ = db.ListProposals(true); len(pending) != 0 {
		t.Errorf("dismissed proposal came back with no change in evidence: %+v", pending)
	}

	// Now the facts change: another month passes, so days_unseen differs.
	later := now.Add(30 * 24 * time.Hour)
	ps, _ = Generate(db, later)
	if _, err := Store(db, ps, later); err != nil {
		t.Fatal(err)
	}
	if pending, _ = db.ListProposals(true); len(pending) != 1 {
		t.Errorf("changed evidence should re-raise, got %d pending", len(pending))
	}
}

func TestPendingCapBoundsTheQueue(t *testing.T) {
	db := openDB(t)
	for i := range ledger.MaxPendingProposals + 5 {
		if err := db.UpsertArtifact(ledger.ArtifactRecord{
			Kind: "skill", Path: fmt.Sprintf("/s/old-%d.md", i), Name: fmt.Sprintf("old%d", i),
		}, now.Add(-StaleAfter-48*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	ps, _ := Generate(db, now)
	if len(ps) != ledger.MaxPendingProposals+5 {
		t.Fatalf("generator produced %d, want %d", len(ps), ledger.MaxPendingProposals+5)
	}
	if _, err := Store(db, ps, now); err != nil {
		t.Fatal(err)
	}

	pending, _ := db.ListProposals(true)
	if len(pending) != ledger.MaxPendingProposals {
		t.Errorf("stored %d pending, want the cap of %d", len(pending), ledger.MaxPendingProposals)
	}
}

func TestHashIsStableAndEvidenceSensitive(t *testing.T) {
	a := Proposal{Kind: "k", Subject: "s", Evidence: map[string]any{"x": 1, "y": "two"}}
	b := Proposal{Kind: "k", Subject: "s", Evidence: map[string]any{"y": "two", "x": 1}}
	if a.Hash() != b.Hash() {
		t.Error("hash must not depend on map ordering, or every run would re-raise everything")
	}
	c := Proposal{Kind: "k", Subject: "s", Evidence: map[string]any{"x": 2, "y": "two"}}
	if a.Hash() == c.Hash() {
		t.Error("changed evidence must change the hash, or a dismissal would never end")
	}
}
