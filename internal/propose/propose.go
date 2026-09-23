// Package propose turns ledger state into reviewable proposals. It generates
// only what the data actually supports: a proposal with no evidence behind it
// is a guess with extra ceremony.
//
// See docs/design.md ("B5 scope"). The split that governs everything here is
// what a proposal touches:
//
//   - the user's files: Loom renders it and never applies it, not once and not
//     with permission, because constraint 8 says Loom does not write there
//   - Loom's own state: applying is defensible, since the blast radius is the
//     ledger and the change reverts in one command
//
// Auto-apply is not implemented in this slice. Generation, storage and listing
// are, which is a useful place to stop.
package propose

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/teerakarna/loom/internal/ledger"
	"github.com/teerakarna/loom/internal/policy"
)

// Proposal kinds.
const (
	// KindRetireAsset suggests removing an asset that has not been seen
	// on disk for a long time. Touches the user's files, so it is rendered and
	// never applied.
	KindRetireAsset = "retire_asset"

	// KindPinModel suggests recording a deliberate model policy for an agent
	// type with enough measured runs behind it. Touches only Loom's own
	// policies table.
	KindPinModel = "pin_model"

	// KindRevertPolicy suggests undoing a policy whose measured results got
	// worse after it was applied. This is what closes the loop: applying a
	// policy writes a baseline, and runs after it are the test of whether the
	// decision held. Touches only Loom's own policies table.
	KindRevertPolicy = "revert_policy"
)

// Loop-closure thresholds, stated rather than implied.
const (
	// MinPostApplyRuns is how many runs must follow a policy before a
	// regression can be claimed. Lower than policy.MinSampleSize deliberately:
	// the asymmetry is that applying moves you to an unproven state while
	// reverting restores a known-good one you already had evidence for, so the
	// bar for going back is lower than the bar for going forward. It is not
	// zero, because a regression declared on two runs is a guess.
	MinPostApplyRuns = 10

	// RegressionCostRatio is how much worse the median cost must get before it
	// counts. 25% absorbs ordinary variance without hiding a real change.
	RegressionCostRatio = 1.25

	// RegressionReworkDelta is the rise in denials or corrections per run that
	// counts as a quality regression. Rework matters more than cost: a cheaper
	// model that gets things wrong is not a saving.
	RegressionReworkDelta = 0.2
)

// TouchesUserFiles reports whether applying a proposal of this kind would
// write outside Loom's own state. Used to decide what may ever be automated;
// the answer for anything touching a user's files is permanently no.
//
// Defaults to true for a kind this function does not recognize - fail safe,
// not fail open. An unrecognized kind isn't necessarily KindPinModel's kind
// of safe; the only way to know it isn't is to name it explicitly here, and
// every kind that actually is safe does (KindPinModel, KindRevertPolicy).
func TouchesUserFiles(kind string) bool {
	switch kind {
	case KindPinModel, KindRevertPolicy:
		return false
	default:
		return true
	}
}

// StaleAfter is how long an asset must be unseen before retirement is even
// suggested. Deliberately generous: a skill used twice a year is not dead, and
// a proposal to delete it would be noise with a confident face on it.
const StaleAfter = 90 * 24 * time.Hour

// Proposal is one generated suggestion, before storage.
type Proposal struct {
	Kind       string
	Subject    string
	Evidence   map[string]any
	SampleSize int
	EffectSize *float64
	// Summary is one line a human reads. Rationale says what would happen and
	// who does it.
	Summary   string
	Rationale string
}

// Hash is the content address of a proposal's evidence. A dismissal survives
// until this changes, so what goes in here decides what counts as "the
// situation changed" - which is why it is the evidence, not the timestamp.
func (p Proposal) Hash() string {
	b, err := json.Marshal(p.Evidence)
	if err != nil {
		// A map that will not marshal is a programming error, but degrading to
		// a stable per-subject hash is better than crashing a read-only
		// command.
		b = []byte(p.Kind + "|" + p.Subject)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Generate produces every proposal the current ledger state supports. Order is
// stable so repeated runs do not reshuffle the list under a reader.
func Generate(db *ledger.DB, now time.Time) ([]Proposal, error) {
	var out []Proposal

	retire, err := retireStaleAssets(db, now)
	if err != nil {
		return nil, err
	}
	out = append(out, retire...)

	pin, err := pinModels(db)
	if err != nil {
		return nil, err
	}
	out = append(out, pin...)

	revert, err := revertRegressedPolicies(db)
	if err != nil {
		return nil, err
	}
	return append(out, revert...), nil
}

// revertRegressedPolicies closes the loop. For every policy applied from
// evidence, it compares runs since that policy was applied against the
// baseline the decision rested on, and proposes going back when the result got
// worse.
//
// Only evidence-sourced policies are checked. A policy set by hand is a
// decision someone made for reasons Loom cannot see, and it is not Loom's
// place to second-guess it with a number.
func revertRegressedPolicies(db *ledger.DB) ([]Proposal, error) {
	stats, err := db.StatsByAgentType()
	if err != nil {
		return nil, err
	}

	var out []Proposal
	for _, s := range stats {
		pol, err := db.GetPolicy(s.AgentType)
		if err != nil {
			return nil, err
		}
		if pol == nil || pol.Source != "evidence" || pol.BaselineMedianCost <= 0 {
			continue
		}
		applied, err := time.Parse(time.RFC3339, pol.CreatedAt)
		if err != nil {
			continue
		}

		since, err := db.StatsByAgentTypeSince(s.AgentType, applied)
		if err != nil {
			return nil, err
		}
		if since.Runs < MinPostApplyRuns {
			continue // not enough evidence since to say anything
		}

		reason, regressed := regressionReason(pol, since)
		if !regressed {
			continue
		}

		effect := since.MedianCost - pol.BaselineMedianCost
		out = append(out, Proposal{
			Kind:    KindRevertPolicy,
			Subject: s.AgentType,
			Evidence: map[string]any{
				"agent_type": s.AgentType, "model": pol.Model,
				"applied_at": pol.CreatedAt, "runs_since": since.Runs,
				"baseline_median_cost": pol.BaselineMedianCost, "since_median_cost": since.MedianCost,
				"baseline_denial_rate": pol.BaselineDenialRate, "since_denial_rate": since.DenialRate,
				"baseline_feedback_rate": pol.BaselineFeedbackRate, "since_feedback_rate": since.FeedbackRate,
				"reason": reason,
			},
			SampleSize: since.Runs,
			EffectSize: &effect,
			Summary:    fmt.Sprintf("revert %s: %s", s.AgentType, reason),
			Rationale: fmt.Sprintf("Pinned to %s on %d runs, but %s over the %d runs since. "+
				"Reverting restores the shipped default, which is the state you had evidence for.",
				pol.Model, pol.SampleSize, reason, since.Runs),
		})
	}
	return out, nil
}

// regressionReason says what got worse, in words a human can act on. Returning
// the reason rather than a bare bool is the difference between "reverted" and
// "reverted because median cost rose 40% over 22 runs".
func regressionReason(pol *ledger.PolicyRow, since ledger.AgentTypeStats) (string, bool) {
	if since.DenialRate-pol.BaselineDenialRate >= RegressionReworkDelta {
		return fmt.Sprintf("tool denials rose from %.2f to %.2f per run",
			pol.BaselineDenialRate, since.DenialRate), true
	}
	if since.FeedbackRate-pol.BaselineFeedbackRate >= RegressionReworkDelta {
		return fmt.Sprintf("corrections rose from %.2f to %.2f per run",
			pol.BaselineFeedbackRate, since.FeedbackRate), true
	}
	if pol.BaselineMedianCost > 0 && since.MedianCost >= pol.BaselineMedianCost*RegressionCostRatio {
		pct := (since.MedianCost/pol.BaselineMedianCost - 1) * 100
		return fmt.Sprintf("median cost rose %.0f%%, from %.0f to %.0f",
			pct, pol.BaselineMedianCost, since.MedianCost), true
	}
	return "", false
}

// retireStaleAssets proposes retiring an asset unused for StaleAfter.
//
// "Unused" is the last time a run actually touched it - issue #39's join -
// falling back to first_seen for an asset discovery has found but no run
// has ever touched under usage tracking. first_seen is stable and never
// reset by a later discovery pass, unlike last_seen.
//
// last_seen itself is never the clock here, on purpose: it is bumped by
// UpsertAsset on every discovery pass, so it resets every time `loom
// advise` runs and can never reach the threshold for anything still on
// disk. Before #39 that meant only a file already deleted from disk could
// ever be proposed for retirement - the exact bug issue #38 recorded, found
// by forcing an asset's clock back and watching one `loom advise` erase
// the evidence. last_seen still answers its own question correctly (is it
// on disk - see MarkStaleAssets); it was never a valid answer to this
// one.
func retireStaleAssets(db *ledger.DB, now time.Time) ([]Proposal, error) {
	rows, err := db.ListAssets()
	if err != nil {
		return nil, err
	}
	usage, err := db.UsageSummary()
	if err != nil {
		return nil, err
	}
	agentUsage, err := db.AgentTypeLastUsed()
	if err != nil {
		return nil, err
	}

	var out []Proposal
	for _, a := range rows {
		clock, everUsed, err := lastUsedOrFirstSeen(a, usage, agentUsage, now)
		if err != nil {
			continue // unparseable timestamp is not grounds for a deletion suggestion
		}
		if now.Sub(clock) < StaleAfter {
			continue
		}
		days := int(now.Sub(clock).Hours() / 24)

		onDisk := a.Status == "active"
		rationale := "Loom will not delete it: this is a suggestion to review and remove yourself."
		switch {
		case !onDisk:
			rationale = "Not found on disk by the last several discovery passes. " + rationale
		case everUsed:
			rationale = fmt.Sprintf("Still on disk, but no recorded run has used it in %d days. ", days) + rationale
		default:
			rationale = "Still on disk, but no recorded run has ever used it. " + rationale
		}

		out = append(out, Proposal{
			Kind:    KindRetireAsset,
			Subject: a.Path,
			Evidence: map[string]any{
				"path": a.Path, "type": a.Kind, "name": a.Name,
				"on_disk": onDisk, "ever_used": everUsed, "days_unused": days,
			},
			SampleSize: 0,
			Summary:    fmt.Sprintf("retire %s %q, unused for %d days", a.Kind, a.Name, days),
			Rationale:  rationale,
		})
	}
	return out, nil
}

// lastUsedOrFirstSeen returns the clock retireStaleAssets measures
// staleness against for a, and whether any run has ever used it (issue
// #39's join). Preferring the last-used time over first_seen is the fix for
// issue #38.
//
// Agent-kind assets are checked against agentUsage (runs.agent_type)
// rather than the asset_usage join: runs.agent_type already existed for
// B3a's policy attribution, so agent usage needs no new signal -
// docs/design.md said so, and this is where that claim is actually wired
// in. Found missing by code review: it was documented but never checked
// here, so a custom agent invoked constantly via the Agent tool (never a
// Skill/Read/Edit/Write call, so never an asset_usage row) could be
// proposed for retirement as "never used" while in active use.
//
// A usage row with real evidence (Uses > 0 / an agent_type with a run) but
// no parseable timestamp - every contributing run had unparseable
// started_at and ended_at, an edge case rather than the common path - is
// treated as used just now, not as never used: real evidence of use with
// an unmeasurable age is safer read as "don't know how stale" than
// "definitely stale".
func lastUsedOrFirstSeen(a ledger.AssetRow, usage map[string]ledger.AssetUsageSummary,
	agentUsage map[string]string, now time.Time) (time.Time, bool, error) {
	if a.Kind == "agent" {
		if lastUsed, ok := agentUsage[a.Name]; ok {
			if lastUsed == "" {
				return now, true, nil
			}
			t, err := time.Parse(time.RFC3339, lastUsed)
			return t, true, err
		}
		t, err := time.Parse(time.RFC3339, a.FirstSeen)
		return t, false, err
	}
	if u, ok := usage[a.Path]; ok {
		if u.LastUsedAt == "" {
			return now, true, nil
		}
		t, err := time.Parse(time.RFC3339, u.LastUsedAt)
		return t, true, err
	}
	t, err := time.Parse(time.RFC3339, a.FirstSeen)
	return t, false, err
}

// pinModels proposes a deliberate model policy for agent types with enough
// measured runs. Only fires past policy.MinSampleSize, so a thin trend cannot
// dress itself up as a recommendation (constraint 11).
func pinModels(db *ledger.DB) ([]Proposal, error) {
	stats, err := db.StatsByAgentType()
	if err != nil {
		return nil, err
	}

	var out []Proposal
	for _, s := range stats {
		if s.Runs < policy.MinSampleSize || s.ObservedModel == "" {
			continue
		}
		existing, err := db.GetPolicy(s.AgentType)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			continue // already a deliberate policy; not Loom's place to relitigate it
		}
		// Guardrail from the design doc: never propose pinning for an agent
		// type whose measured rework rate is already elevated. A cheaper or
		// different model is the wrong lever when the problem is quality.
		if s.DenialRate > 0.5 || s.FeedbackRate > 0.5 {
			continue
		}
		effect := s.MedianCost
		out = append(out, Proposal{
			Kind:    KindPinModel,
			Subject: s.AgentType,
			Evidence: map[string]any{
				"agent_type": s.AgentType, "runs": s.Runs,
				"median_cost": s.MedianCost, "median_tools": s.MedianTools,
				"observed_model": s.ObservedModel,
				"denial_rate":    s.DenialRate, "feedback_rate": s.FeedbackRate,
			},
			SampleSize: s.Runs,
			EffectSize: &effect,
			Summary: fmt.Sprintf("pin %s to %s, measured over %d runs",
				s.AgentType, s.ObservedModel, s.Runs),
			Rationale: fmt.Sprintf("Median cost %.0f over %d runs, with no elevated rework. "+
				"Applying writes only to loom's own policy table, and `loom policy unset %s` reverts it.",
				s.MedianCost, s.Runs, s.AgentType),
		})
	}
	return out, nil
}

// Apply carries out one proposal. It refuses anything that touches the user's
// files, unconditionally: Loom does not write there, and "the human applies"
// is not a setting.
//
// This is deliberately a command a person runs, not automation. Auto-apply was
// considered and rejected on the arithmetic: a pin is suppressed once a policy
// exists, so it can fire at most once per agent type, ever - two times on the
// corpus it was measured against - and each firing saves exactly one command.
// That does not justify preference storage, window caps and unattended writes.
// What it does justify is not having to retype a model name off a proposal,
// which is what this gives.
func Apply(db *ledger.DB, id int64, now time.Time) (string, error) {
	p, err := db.GetProposal(id)
	if err != nil {
		return "", err
	}
	if p == nil {
		return "", fmt.Errorf("no proposal #%d", id)
	}
	if p.Status == ledger.ProposalApplied {
		return "", fmt.Errorf("#%d has already been applied", id)
	}
	if p.Status == ledger.ProposalWithdrawn {
		return "", fmt.Errorf("#%d was withdrawn - the evidence it rested on no longer holds, so "+
			"applying it would act on a fact that is no longer true", id)
	}
	if TouchesUserFiles(p.Kind) {
		return "", fmt.Errorf("#%d touches your files, so loom will not apply it. "+
			"It is a suggestion to review and act on yourself", id)
	}

	var ev map[string]any
	if err := json.Unmarshal([]byte(p.Evidence), &ev); err != nil {
		return "", fmt.Errorf("reading evidence for #%d: %w", id, err)
	}

	switch p.Kind {
	case KindPinModel:
		model, _ := ev["observed_model"].(string)
		if model == "" {
			return "", fmt.Errorf("#%d has no model in its evidence", id)
		}
		// Record what this decision was measured against. Without the
		// baseline the loop cannot close: there would be nothing to compare
		// later runs to, and "did this help" would be unanswerable.
		if err := db.UpsertPolicy(ledger.PolicyRow{
			CriteriaVersion:      policy.CriteriaVersion,
			AgentType:            p.Subject,
			Model:                model,
			Effort:               "",
			Source:               "evidence",
			SampleSize:           p.SampleSize,
			BaselineMedianCost:   asFloat(ev["median_cost"]),
			BaselineDenialRate:   asFloat(ev["denial_rate"]),
			BaselineFeedbackRate: asFloat(ev["feedback_rate"]),
		}, now); err != nil {
			return "", err
		}
		if err := db.MarkProposalApplied(id); err != nil {
			return "", err
		}
		return fmt.Sprintf("Pinned %s to %s, on %d measured runs.\n"+
			"Revert with: loom policy unset %s", p.Subject, model, p.SampleSize, p.Subject), nil
	case KindRevertPolicy:
		if err := db.DeletePolicy(p.Subject); err != nil {
			return "", err
		}
		if err := db.MarkProposalApplied(id); err != nil {
			return "", err
		}
		reason, _ := ev["reason"].(string)
		// Reverting re-opens the question rather than settling it: with no
		// policy in place, the original pin becomes proposable again once the
		// evidence supports it. That is the loop closing and re-opening, which
		// is the point.
		return fmt.Sprintf("Reverted %s to the shipped default.\nWhy: %s.\n"+
			"The pin can be proposed again once the evidence supports it.", p.Subject, reason), nil

	default:
		return "", fmt.Errorf("loom does not know how to apply a %q proposal", p.Kind)
	}
}

// asFloat reads a number out of decoded JSON evidence, where every number is a
// float64. Missing or wrong-typed values become 0, which for a baseline means
// "no baseline recorded" and correctly disables regression checking rather
// than inventing a comparison.
func asFloat(v any) float64 {
	f, _ := v.(float64)
	return f
}

// Store writes generated proposals to the ledger, applying the dedupe rule.
// Reports how many were newly raised or re-raised.
//
// Withdraws stale pending proposals first (issue #40): anything ps does not
// contain has evidence the ledger no longer supports, and a proposal that
// stopped applying is not the same event as one a human dismissed. Doing
// this before the upsert loop below, not after, is what lets a proposal
// freed by a withdrawal fill the same pass's MaxPendingProposals slot.
func Store(db *ledger.DB, ps []Proposal, now time.Time) (int, error) {
	generated := make(map[string]bool, len(ps))
	for _, p := range ps {
		generated[p.Kind+"|"+p.Subject] = true
	}
	if err := db.WithdrawStalePending(generated); err != nil {
		return 0, err
	}

	written := 0
	for _, p := range ps {
		ev, err := json.Marshal(p.Evidence)
		if err != nil {
			return written, err
		}
		ok, err := db.UpsertProposal(ledger.ProposalRow{
			Kind: p.Kind, Subject: p.Subject,
			Evidence: string(ev), EvidenceHash: p.Hash(),
			SampleSize: p.SampleSize, EffectSize: p.EffectSize,
		}, now)
		if err != nil {
			return written, err
		}
		if ok {
			written++
		}
	}
	return written, nil
}
