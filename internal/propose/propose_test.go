package propose

import (
	"fmt"
	"path/filepath"
	"strings"
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

// TestRetireFiresForSomethingStillOnDiskButUnused is the regression test
// for issue #38: UpsertArtifact bumps last_seen on every discovery pass, so
// before #39's usage join existed, only a file already deleted from disk
// could ever reach the staleness threshold - repeated discovery (`loom
// advise` running before every recommendation) reset the clock on anything
// still present, forever. This reproduces exactly that: discovery runs
// three times, weeks apart, on an artifact nothing ever uses, and the
// artifact stays present (status "active") throughout.
func TestRetireFiresForSomethingStillOnDiskButUnused(t *testing.T) {
	db := openDB(t)
	firstSeen := now.Add(-StaleAfter - 48*time.Hour)
	if err := db.UpsertArtifact(ledger.ArtifactRecord{Kind: "skill", Path: "/s/ignored.md", Name: "ignored"}, firstSeen); err != nil {
		t.Fatal(err)
	}
	// Two more discovery passes, most recently just before `now` - old
	// last_seen-based logic would compute an age of hours, not days, and
	// never propose this.
	if err := db.UpsertArtifact(ledger.ArtifactRecord{Kind: "skill", Path: "/s/ignored.md", Name: "ignored"}, firstSeen.Add(30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertArtifact(ledger.ArtifactRecord{Kind: "skill", Path: "/s/ignored.md", Name: "ignored"}, now.Add(-1*time.Hour)); err != nil {
		t.Fatal(err)
	}

	rows, err := db.ListArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Status != "active" {
		t.Fatalf("artifact status = %q, want active (still on disk is the whole point of this test)", rows[0].Status)
	}

	ps, err := Generate(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Subject != "/s/ignored.md" {
		t.Fatalf("got %+v, want one retire proposal for /s/ignored.md", ps)
	}
	if onDisk, _ := ps[0].Evidence["on_disk"].(bool); !onDisk {
		t.Error("evidence must say this is still on disk, not imply it was deleted")
	}
}

// TestRetireNotProposedWhenRecentlyUsed is the flip side: an artifact whose
// first_seen is old enough to be stale on its own, but that a run genuinely
// touched recently, must not be proposed. Usage is the signal that matters,
// not how long ago discovery first found it.
func TestRetireNotProposedWhenRecentlyUsed(t *testing.T) {
	db := openDB(t)
	if err := db.UpsertArtifact(ledger.ArtifactRecord{Kind: "skill", Path: "/s/used.md", Name: "used"},
		now.Add(-StaleAfter-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertRun(ledger.RunRecord{Path: "/run/a.jsonl", Kind: "session", StartedAt: now.Add(-1 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	runID, err := db.RunIDByPath("/run/a.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceArtifactUsage(runID, []ledger.ResolvedTouch{{ToolUseID: "t1", ArtifactPath: "/s/used.md"}}); err != nil {
		t.Fatal(err)
	}

	ps, err := Generate(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 0 {
		t.Errorf("got %+v, want no proposals - this artifact was used an hour ago", ps)
	}
}

// TestRetireNotProposedForActivelyInvokedAgent is the regression test for a
// bug code review found: docs/design.md claims agent usage needs no new
// signal because runs.agent_type already exists (B3a), but
// retireStaleArtifacts never actually checked it - only the artifact_usage
// join (Skill/Read/Edit/Write signals, which an Agent tool_use is none of).
// A custom agent artifact with a stale first_seen but real, recent runs
// under its agent_type must not be proposed for retirement.
func TestRetireNotProposedForActivelyInvokedAgent(t *testing.T) {
	db := openDB(t)
	if err := db.UpsertArtifact(ledger.ArtifactRecord{Kind: "agent", Path: "/agents/reviewer.md", Name: "reviewer"},
		now.Add(-StaleAfter-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertRun(ledger.RunRecord{
		Path: "/run/reviewer-1.jsonl", Kind: "agent", AgentType: "reviewer", StartedAt: now.Add(-1 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	ps, err := Generate(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 0 {
		t.Errorf("got %+v, want no proposals - this agent was invoked an hour ago", ps)
	}
}

// TestRetireProposedForAgentNeverInvoked is the flip side: an agent artifact
// discovered on disk but with no runs recorded under its agent_type falls
// back to first_seen, same as any other never-used artifact.
func TestRetireProposedForAgentNeverInvoked(t *testing.T) {
	db := openDB(t)
	if err := db.UpsertArtifact(ledger.ArtifactRecord{Kind: "agent", Path: "/agents/unused.md", Name: "unused"},
		now.Add(-StaleAfter-48*time.Hour)); err != nil {
		t.Fatal(err)
	}

	ps, err := Generate(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Subject != "/agents/unused.md" {
		t.Fatalf("got %+v, want one retire proposal for /agents/unused.md", ps)
	}
	if everUsed, _ := ps[0].Evidence["ever_used"].(bool); everUsed {
		t.Error("evidence must say this agent was never used")
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

// The rule that is not a setting: Loom refuses to apply anything touching the
// user's files, and refusing is not an error the caller can configure away.
func TestApplyRefusesAnythingTouchingUserFiles(t *testing.T) {
	db := openDB(t)
	if err := db.UpsertArtifact(ledger.ArtifactRecord{Kind: "skill", Path: "/s/old.md", Name: "old"},
		now.Add(-StaleAfter-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	ps, _ := Generate(db, now)
	if _, err := Store(db, ps, now); err != nil {
		t.Fatal(err)
	}
	pending, _ := db.ListProposals(true)

	_, err := Apply(db, pending[0].ID, now)
	if err == nil {
		t.Fatal("expected a refusal for a proposal that touches user files")
	}
	// And the artifact is untouched: the refusal is not a partial apply.
	arts, _ := db.ListArtifacts()
	if len(arts) != 1 {
		t.Errorf("artifact list changed despite the refusal: %+v", arts)
	}
}

func TestApplyPinsTheModelAndIsRevertible(t *testing.T) {
	db := openDB(t)
	seedAgentRuns(t, db, "Explore", policy.MinSampleSize, 0)
	ps, _ := Generate(db, now)
	if _, err := Store(db, ps, now); err != nil {
		t.Fatal(err)
	}
	pending, _ := db.ListProposals(true)
	if len(pending) != 1 {
		t.Fatalf("setup: got %d pending, want 1", len(pending))
	}

	msg, err := Apply(db, pending[0].ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if msg == "" {
		t.Error("apply should say what it did")
	}

	// The policy exists, and records that evidence rather than a human set it.
	pol, err := db.GetPolicy("Explore")
	if err != nil || pol == nil {
		t.Fatalf("GetPolicy = (%v, %v), want a row", pol, err)
	}
	if pol.Source != "evidence" {
		t.Errorf("Source = %q, want evidence", pol.Source)
	}
	if pol.SampleSize != policy.MinSampleSize {
		t.Errorf("SampleSize = %d, want %d", pol.SampleSize, policy.MinSampleSize)
	}

	// Applied, not merely hidden: the record says what happened.
	got, _ := db.GetProposal(pending[0].ID)
	if got.Status != ledger.ProposalApplied {
		t.Errorf("status = %q, want %q", got.Status, ledger.ProposalApplied)
	}

	// And it is not proposed again, because a policy now exists.
	ps, _ = Generate(db, now)
	for _, p := range ps {
		if p.Kind == KindPinModel && p.Subject == "Explore" {
			t.Error("re-proposed a pin that has already been applied")
		}
	}

	// Revert works, which is what makes applying defensible at all.
	if err := db.DeletePolicy("Explore"); err != nil {
		t.Fatal(err)
	}
	if pol, _ := db.GetPolicy("Explore"); pol != nil {
		t.Error("revert left the policy in place")
	}
}

func TestApplyRejectsUnknownAndRepeatIDs(t *testing.T) {
	db := openDB(t)
	if _, err := Apply(db, 999, now); err == nil {
		t.Error("expected an error for an unknown proposal id")
	}

	seedAgentRuns(t, db, "Explore", policy.MinSampleSize, 0)
	ps, _ := Generate(db, now)
	if _, err := Store(db, ps, now); err != nil {
		t.Fatal(err)
	}
	pending, _ := db.ListProposals(true)
	if _, err := Apply(db, pending[0].ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(db, pending[0].ID, now); err == nil {
		t.Error("applying twice should be refused, not silently repeated")
	}
}

// seedAgentRunsAt seeds runs with an explicit start time, so before/after a
// marker can be distinguished.
func seedAgentRunsAt(t *testing.T, db *ledger.DB, agentType string, n int, cost float64, denials int, at time.Time, prefix string) {
	t.Helper()
	for i := range n {
		d := 0
		if i < denials {
			d = 1
		}
		if err := db.InsertRun(ledger.RunRecord{
			Path: fmt.Sprintf("/%s/%s-%d.jsonl", prefix, agentType, i), Kind: "agent",
			AgentType: agentType, Model: "claude-haiku-4-5",
			WeightedCost: cost, ToolUseCount: 3, DenialCount: d,
			StartedAt: at, EndedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// applyAPin drives the real path: propose, then apply, so the baseline is
// recorded the way it would be in use rather than hand-written.
func applyAPin(t *testing.T, db *ledger.DB, agentType string, at time.Time) {
	t.Helper()
	ps, err := Generate(db, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Store(db, ps, at); err != nil {
		t.Fatal(err)
	}
	pending, _ := db.ListProposals(true)
	for _, p := range pending {
		if p.Kind == KindPinModel && p.Subject == agentType {
			if _, err := Apply(db, p.ID, at); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("no pin proposal to apply for %s", agentType)
}

func TestApplyRecordsTheBaselineThatClosesTheLoop(t *testing.T) {
	db := openDB(t)
	before := now.Add(-30 * 24 * time.Hour)
	seedAgentRunsAt(t, db, "Explore", policy.MinSampleSize, 100, 0, before, "pre")
	applyAPin(t, db, "Explore", now)

	pol, err := db.GetPolicy("Explore")
	if err != nil || pol == nil {
		t.Fatalf("GetPolicy = (%v, %v)", pol, err)
	}
	if pol.BaselineMedianCost != 100 {
		t.Errorf("BaselineMedianCost = %v, want 100 - without it the loop cannot close", pol.BaselineMedianCost)
	}
}

func TestNoRevertProposedBeforeEnoughRunsSince(t *testing.T) {
	db := openDB(t)
	before := now.Add(-30 * 24 * time.Hour)
	seedAgentRunsAt(t, db, "Explore", policy.MinSampleSize, 100, 0, before, "pre")
	applyAPin(t, db, "Explore", now)

	// Far worse, but too few runs to say so.
	seedAgentRunsAt(t, db, "Explore", MinPostApplyRuns-1, 1000, 0, now.Add(time.Hour), "post")

	ps, err := Generate(db, now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.Kind == KindRevertPolicy {
			t.Errorf("declared a regression on %d runs, below the %d minimum", MinPostApplyRuns-1, MinPostApplyRuns)
		}
	}
}

func TestRevertProposedWhenCostRegresses(t *testing.T) {
	db := openDB(t)
	before := now.Add(-30 * 24 * time.Hour)
	seedAgentRunsAt(t, db, "Explore", policy.MinSampleSize, 100, 0, before, "pre")
	applyAPin(t, db, "Explore", now)

	// Clearly worse, with enough runs behind it.
	seedAgentRunsAt(t, db, "Explore", MinPostApplyRuns, 200, 0, now.Add(time.Hour), "post")

	ps, err := Generate(db, now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var rev *Proposal
	for i := range ps {
		if ps[i].Kind == KindRevertPolicy {
			rev = &ps[i]
		}
	}
	if rev == nil {
		t.Fatal("expected a revert proposal after a clear cost regression")
	}
	if TouchesUserFiles(rev.Kind) {
		t.Error("a revert touches only loom's own state")
	}
	// The reason must be actionable, not a bare flag.
	if !strings.Contains(rev.Summary, "median cost rose") {
		t.Errorf("summary should say what got worse: %q", rev.Summary)
	}
}

func TestRevertNotProposedWhenResultsHeld(t *testing.T) {
	db := openDB(t)
	before := now.Add(-30 * 24 * time.Hour)
	seedAgentRunsAt(t, db, "Explore", policy.MinSampleSize, 100, 0, before, "pre")
	applyAPin(t, db, "Explore", now)
	// Slightly better, plenty of runs.
	seedAgentRunsAt(t, db, "Explore", MinPostApplyRuns*2, 90, 0, now.Add(time.Hour), "post")

	ps, err := Generate(db, now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.Kind == KindRevertPolicy {
			t.Errorf("proposed a revert when results held or improved: %+v", p)
		}
	}
}

// Rework matters more than cost: a cheaper model that gets things wrong is not
// a saving.
func TestRevertProposedWhenReworkRises(t *testing.T) {
	db := openDB(t)
	before := now.Add(-30 * 24 * time.Hour)
	seedAgentRunsAt(t, db, "Explore", policy.MinSampleSize, 100, 0, before, "pre")
	applyAPin(t, db, "Explore", now)
	// Cheaper, but denials on every run.
	seedAgentRunsAt(t, db, "Explore", MinPostApplyRuns, 50, MinPostApplyRuns, now.Add(time.Hour), "post")

	ps, err := Generate(db, now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range ps {
		if p.Kind == KindRevertPolicy {
			found = true
			if !strings.Contains(p.Summary, "denials rose") {
				t.Errorf("expected the denial rate to be named: %q", p.Summary)
			}
		}
	}
	if !found {
		t.Error("cheaper but with rework on every run should still propose a revert")
	}
}

// The loop must close AND re-open: applying a revert removes the policy, which
// makes the original pin proposable again.
func TestApplyingARevertReopensTheQuestion(t *testing.T) {
	db := openDB(t)
	before := now.Add(-30 * 24 * time.Hour)
	seedAgentRunsAt(t, db, "Explore", policy.MinSampleSize, 100, 0, before, "pre")
	applyAPin(t, db, "Explore", now)
	seedAgentRunsAt(t, db, "Explore", MinPostApplyRuns, 200, 0, now.Add(time.Hour), "post")

	later := now.Add(2 * time.Hour)
	ps, _ := Generate(db, later)
	if _, err := Store(db, ps, later); err != nil {
		t.Fatal(err)
	}
	pending, _ := db.ListProposals(true)
	var revertID int64
	for _, p := range pending {
		if p.Kind == KindRevertPolicy {
			revertID = p.ID
		}
	}
	if revertID == 0 {
		t.Fatal("no revert proposal to apply")
	}

	msg, err := Apply(db, revertID, later)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "Why:") {
		t.Errorf("a revert must say why, not just that: %q", msg)
	}
	if pol, _ := db.GetPolicy("Explore"); pol != nil {
		t.Error("the policy should be gone after reverting")
	}

	// And the pin becomes proposable again.
	ps, _ = Generate(db, later)
	reopened := false
	for _, p := range ps {
		if p.Kind == KindPinModel && p.Subject == "Explore" {
			reopened = true
		}
	}
	if !reopened {
		t.Error("reverting should re-open the question, not settle it")
	}
}

// A policy someone set by hand is a decision Loom cannot see the reasons for.
func TestHandSetPolicyIsNeverSecondGuessed(t *testing.T) {
	db := openDB(t)
	before := now.Add(-30 * 24 * time.Hour)
	seedAgentRunsAt(t, db, "Explore", policy.MinSampleSize, 100, 0, before, "pre")
	if err := db.UpsertPolicy(ledger.PolicyRow{
		CriteriaVersion: "v1", AgentType: "Explore", Model: "opus", Effort: "high", Source: "human",
	}, now); err != nil {
		t.Fatal(err)
	}
	seedAgentRunsAt(t, db, "Explore", MinPostApplyRuns*3, 100000, MinPostApplyRuns*3, now.Add(time.Hour), "post")

	ps, err := Generate(db, now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.Kind == KindRevertPolicy {
			t.Error("second-guessed a hand-set policy with a number")
		}
	}
}
