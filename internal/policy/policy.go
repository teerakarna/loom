package policy

import (
	"fmt"

	"github.com/teerakarna/loom/internal/ledger"
)

// CriteriaVersion stamps every decision this package produces, so a policy
// written today remains traceable to the rules that produced it after those
// rules change. Bump it whenever defaults or thresholds below change.
const CriteriaVersion = "2026-09-12.1"

// MinSampleSize is the brake on evidence-based claims. Below this many runs
// for an agent type, measured data is reported but never allowed to displace
// a default, because a recommendation from four runs is a guess wearing a
// number's clothing.
//
// 20 is a judgement call, not a derived figure, and is deliberately higher
// than any agent type on the author's own corpus at the time of writing: the
// evidence path ships exercised but unused rather than shipping a mechanism
// that immediately acts on data too thin to support it.
const MinSampleSize = 20

// Decision sources, in the order Resolve prefers them.
const (
	// SourceStored is a deliberate policy someone set, or that was derived
	// from sufficient evidence and applied. It always wins: a human decision
	// is not overridden by a trend.
	SourceStored = "stored"
	// SourceEvidence is derived from measured runs meeting MinSampleSize.
	SourceEvidence = "evidence"
	// SourceDefault is a shipped guess. Honest, and clearly labelled as such.
	SourceDefault = "default"
)

// Decision is an answer plus everything needed to judge how much to trust it.
// Source and SampleSize are not decoration: constraint 11 requires a figure
// derived from one run to be distinguishable from one derived from fifty, and
// this is where that is enforced for routing decisions.
type Decision struct {
	AgentType  string
	Model      string
	Effort     string
	Source     string
	SampleSize int // runs behind the decision; 0 for a shipped default
	Rationale  string
}

// TrustedEvidence reports whether this decision rests on enough measured runs
// to be treated as evidence rather than a starting guess.
func (d Decision) TrustedEvidence() bool {
	return d.Source != SourceDefault && d.SampleSize >= MinSampleSize
}

// defaultFor maps the few agent types whose purpose is unambiguous from their
// name onto the design doc's cold-start rule: "cheaper model for well-scoped
// retrieval and mechanical execution, stronger model for planning and
// ambiguous synthesis".
//
// Deliberately short. Guessing at agent types Loom has never seen is exactly
// the assumption constraint 2 forbids, so anything not listed gets the middle
// option and says so.
var defaultFor = map[string]struct{ model, effort, why string }{
	"Explore": {"haiku", "low", "read-only search and retrieval, well-scoped by construction"},
	"Plan":    {"opus", "high", "planning and ambiguous synthesis, where a weak answer is expensive"},
}

const (
	fallbackModel  = "sonnet"
	fallbackEffort = "medium"
)

// Resolve decides the model and effort for one agent type. stored may be nil
// (no deliberate policy), and stats may be zero (no runs seen).
//
// Order: a stored policy wins, then evidence meeting MinSampleSize, then the
// shipped default. Evidence below the threshold does not silently influence
// the answer; it is reported in the rationale so the reader can see how close
// the tool is to having something to say.
func Resolve(agentType string, stored *ledger.PolicyRow, stats *ledger.AgentTypeStats) Decision {
	if stored != nil {
		return Decision{
			AgentType: agentType, Model: stored.Model, Effort: stored.Effort,
			Source: SourceStored, SampleSize: stored.SampleSize,
			Rationale: fmt.Sprintf("set deliberately (%s, criteria %s)", stored.Source, stored.CriteriaVersion),
		}
	}

	d := Decision{AgentType: agentType, Source: SourceDefault}
	if def, ok := defaultFor[agentType]; ok {
		d.Model, d.Effort = def.model, def.effort
		d.Rationale = "shipped default: " + def.why
	} else {
		d.Model, d.Effort = fallbackModel, fallbackEffort
		d.Rationale = "shipped default: no rule for this agent type, so the middle option rather than a guess"
	}

	if stats == nil || stats.Runs == 0 {
		d.Rationale += "; no runs measured yet"
		return d
	}

	d.SampleSize = stats.Runs
	if stats.Runs < MinSampleSize {
		d.Rationale += fmt.Sprintf("; %d run(s) measured, need %d before evidence displaces this", stats.Runs, MinSampleSize)
		return d
	}

	// Enough evidence to speak. Note this path is reachable but, on small
	// corpora, not reached: that is the intended behaviour, not a gap.
	d.Source = SourceEvidence
	d.Rationale = fmt.Sprintf("measured over %d runs (median cost %.0f, median %.0f tool calls)",
		stats.Runs, stats.MedianCost, stats.MedianTools)
	if stats.ObservedModel != "" {
		d.Model = stats.ObservedModel
		d.Rationale += "; model is what these runs actually used"
	}
	return d
}
