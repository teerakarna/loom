// Package propose turns ledger state into reviewable proposals. It generates
// only what the data actually supports: a proposal with no evidence behind it
// is a guess with extra ceremony.
//
// See docs/design.md ("B5 scope"). The split that governs everything here is
// what a proposal touches:
//
//   - the user's files: Loom renders it and never applies it, not once and not
//     with permission, because constraint 9 says Loom does not write there
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
	// KindRetireArtifact suggests removing an artifact that has not been seen
	// on disk for a long time. Touches the user's files, so it is rendered and
	// never applied.
	KindRetireArtifact = "retire_artifact"

	// KindPinModel suggests recording a deliberate model policy for an agent
	// type with enough measured runs behind it. Touches only Loom's own
	// policies table.
	KindPinModel = "pin_model"
)

// TouchesUserFiles reports whether applying a proposal of this kind would
// write outside Loom's own state. Used to decide what may ever be automated;
// the answer for anything touching a user's files is permanently no.
func TouchesUserFiles(kind string) bool {
	return kind == KindRetireArtifact
}

// StaleAfter is how long an artifact must be unseen before retirement is even
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

	retire, err := retireStaleArtifacts(db, now)
	if err != nil {
		return nil, err
	}
	out = append(out, retire...)

	pin, err := pinModels(db)
	if err != nil {
		return nil, err
	}
	return append(out, pin...), nil
}

// retireStaleArtifacts proposes retiring artifacts unseen for StaleAfter.
// Evidence is the last-seen date, so re-running does not churn the hash, but
// the artifact reappearing on disk does change it.
func retireStaleArtifacts(db *ledger.DB, now time.Time) ([]Proposal, error) {
	rows, err := db.ListArtifacts()
	if err != nil {
		return nil, err
	}

	var out []Proposal
	for _, a := range rows {
		lastSeen, err := time.Parse(time.RFC3339, a.LastSeen)
		if err != nil {
			continue // unparseable timestamp is not grounds for a deletion suggestion
		}
		days := int(now.Sub(lastSeen).Hours() / 24)
		if now.Sub(lastSeen) < StaleAfter {
			continue
		}
		out = append(out, Proposal{
			Kind:    KindRetireArtifact,
			Subject: a.Path,
			Evidence: map[string]any{
				"path": a.Path, "type": a.Kind, "name": a.Name,
				"last_seen": a.LastSeen, "days_unseen": days,
			},
			SampleSize: 0,
			Summary:    fmt.Sprintf("retire %s %q, unseen for %d days", a.Kind, a.Name, days),
			Rationale: "Not found on disk by the last several discovery passes. " +
				"Loom will not delete it: this is a suggestion to review and remove yourself.",
		})
	}
	return out, nil
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
		if err := db.UpsertPolicy(ledger.PolicyRow{
			CriteriaVersion: policy.CriteriaVersion,
			AgentType:       p.Subject,
			Model:           model,
			Effort:          "",
			Source:          "evidence",
			SampleSize:      p.SampleSize,
		}, now); err != nil {
			return "", err
		}
		if err := db.MarkProposalApplied(id); err != nil {
			return "", err
		}
		return fmt.Sprintf("Pinned %s to %s, on %d measured runs.\n"+
			"Revert with: loom policy unset %s", p.Subject, model, p.SampleSize, p.Subject), nil
	default:
		return "", fmt.Errorf("loom does not know how to apply a %q proposal", p.Kind)
	}
}

// Store writes generated proposals to the ledger, applying the dedupe rule.
// Reports how many were newly raised or re-raised.
func Store(db *ledger.DB, ps []Proposal, now time.Time) (int, error) {
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
