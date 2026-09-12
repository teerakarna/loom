package policy

import (
	"strings"
	"testing"

	"github.com/teerakarna/loom/internal/ledger"
)

func TestResolveDefaultWithNoRuns(t *testing.T) {
	d := Resolve("Explore", nil, nil)
	if d.Source != SourceDefault {
		t.Errorf("Source = %q, want %q", d.Source, SourceDefault)
	}
	if d.Model != "haiku" {
		t.Errorf("Model = %q, want haiku for a retrieval agent", d.Model)
	}
	if d.SampleSize != 0 {
		t.Errorf("SampleSize = %d, want 0", d.SampleSize)
	}
	if d.TrustedEvidence() {
		t.Error("a shipped default must never report as trusted evidence")
	}
	if !strings.Contains(d.Rationale, "no runs measured") {
		t.Errorf("rationale should say there is no evidence yet: %q", d.Rationale)
	}
}

func TestResolveUnknownAgentTypeGetsMiddleOption(t *testing.T) {
	d := Resolve("SomethingLoomHasNeverSeen", nil, nil)
	if d.Model != fallbackModel || d.Effort != fallbackEffort {
		t.Errorf("got %s/%s, want the middle option %s/%s", d.Model, d.Effort, fallbackModel, fallbackEffort)
	}
	if !strings.Contains(d.Rationale, "no rule for this agent type") {
		t.Errorf("rationale should admit there is no rule: %q", d.Rationale)
	}
}

// The load-bearing test for constraint 11: thin evidence must not displace a
// default, and must not present itself as a finding.
func TestResolveEvidenceBelowThresholdDoesNotDisplaceDefault(t *testing.T) {
	stats := &ledger.AgentTypeStats{
		AgentType: "Explore", Runs: MinSampleSize - 1,
		MedianCost: 57301, ObservedModel: "claude-sonnet-5",
	}
	d := Resolve("Explore", nil, stats)

	if d.Source != SourceDefault {
		t.Errorf("Source = %q, want %q: %d runs is below the %d threshold", d.Source, SourceDefault, stats.Runs, MinSampleSize)
	}
	if d.Model != "haiku" {
		t.Errorf("Model = %q, want the default haiku, not the observed model", d.Model)
	}
	if d.TrustedEvidence() {
		t.Error("below-threshold evidence must not report as trusted")
	}
	// The sample size is still reported, so a reader can see how close it is.
	if d.SampleSize != stats.Runs {
		t.Errorf("SampleSize = %d, want %d reported even when unused", d.SampleSize, stats.Runs)
	}
	if !strings.Contains(d.Rationale, "before evidence displaces this") {
		t.Errorf("rationale should say what is still needed: %q", d.Rationale)
	}
}

func TestResolveEvidenceAtThresholdIsUsed(t *testing.T) {
	stats := &ledger.AgentTypeStats{
		AgentType: "Explore", Runs: MinSampleSize,
		MedianCost: 1234, MedianTools: 7, ObservedModel: "claude-haiku-4-5",
	}
	d := Resolve("Explore", nil, stats)

	if d.Source != SourceEvidence {
		t.Errorf("Source = %q, want %q at exactly the threshold", d.Source, SourceEvidence)
	}
	if !d.TrustedEvidence() {
		t.Error("evidence at the threshold should report as trusted")
	}
	if d.Model != "claude-haiku-4-5" {
		t.Errorf("Model = %q, want the observed model once evidence is trusted", d.Model)
	}
}

func TestResolveStoredPolicyWins(t *testing.T) {
	stored := &ledger.PolicyRow{
		AgentType: "Explore", Model: "opus", Effort: "high",
		Source: "human", CriteriaVersion: "test-version",
	}
	// Plenty of evidence pointing elsewhere.
	stats := &ledger.AgentTypeStats{AgentType: "Explore", Runs: 500, ObservedModel: "haiku"}

	d := Resolve("Explore", stored, stats)
	if d.Source != SourceStored {
		t.Errorf("Source = %q, want %q: a deliberate setting is not overridden by a trend", d.Source, SourceStored)
	}
	if d.Model != "opus" {
		t.Errorf("Model = %q, want the stored opus", d.Model)
	}
}
