package selector

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/teerakarna/loom/internal/ledger"
)

// TaskDescriptor is the caller's description of what they're about to do,
// the same free-text a user would type as a prompt, or an agent-spawn
// description. There is no structured task-type field: forcing a taxonomy
// onto every caller would violate design doc constraint 2 (discover, never
// assume) on the input side, not just asset discovery.
type TaskDescriptor struct {
	Text string
}

// SkillMatch is one discovered asset scored against a TaskDescriptor.
type SkillMatch struct {
	Kind        string
	Name        string
	Path        string
	Description string
	Score       float64 // 0 (no overlap) to 1 (every descriptor word matched)
	// Suspicious is a best-effort, advisory-only flag (docs/design.md
	// constraint 9): Description is read verbatim off disk from a file Loom
	// doesn't control, and re-served through the MCP server into whatever
	// session asked for a recommendation. This is never a filter, a
	// flagged match is still returned, just with a warning attached, and
	// it is not a security boundary: see LooksSuspicious.
	Suspicious bool
}

// Recommendation is what Recommend returns: which existing assets are
// relevant, and a cold-start model/effort suggestion with its reasoning made
// explicit, per docs/design.md ("Cold start": "the selector uses shipped
// defaults reasoned from first principles ... and says that is what it is
// doing").
type Recommendation struct {
	Matches []SkillMatch
	// BelowThreshold is true when nothing cleared minScore, and Matches
	// holds the best-scoring candidates anyway (issue #64: returning
	// nothing at all when the store plainly holds relevant material is
	// worse than a weak, clearly-labelled guess the caller can discard).
	// False, and Matches empty, means there was genuinely nothing to show
	// - not even a weak one.
	BelowThreshold bool
	Model          string
	Effort         string
	Rationale      string
}

// maxMatches caps how many asset matches Recommend returns, so a caller
// (human or MCP client) gets a short, actionable list rather than every
// asset with any word in common.
const maxMatches = 5

// minScore excludes matches too weak to be worth surfacing, an overlap of a
// single common word out of a long descriptor isn't a recommendation.
const minScore = 0.2

var tokenPattern = regexp.MustCompile(`[a-z0-9]+`)

// stopwords are dropped before scoring. Real task descriptors and skill
// descriptions are full sentences, not keyword lists, without this, two
// wholly unrelated pieces of text sharing a handful of function words (found
// empirically: "the", "a", "and", "for" alone pushed an unrelated skill's
// score above minScore) look like a match. This isn't a hypothetical edge
// case; it happened running Recommend against this machine's real skills.
// A literal map, not derived via tokenize, tokenize itself filters against
// this set, so building it by tokenizing would be a self-referential
// initialization cycle.
var stopwords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "of": true,
	"to": true, "in": true, "on": true, "for": true, "with": true, "that": true,
	"this": true, "is": true, "are": true, "be": true, "as": true, "it": true,
	"at": true, "by": true, "from": true, "into": true, "about": true, "use": true,
	"when": true, "working": true, "her": true, "his": true, "its": true,
	"your": true, "our": true, "their": true,
}

// tokenize lowercases s and splits it into a set of word tokens, dropping
// punctuation and stopwords. A set (not a slice) because scoring only cares
// which distinct words overlap, not how many times a word repeats.
func tokenize(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range tokenPattern.FindAllString(strings.ToLower(s), -1) {
		if stopwords[w] {
			continue
		}
		out[w] = true
	}
	return out
}

// tokenizeWithCompounds is tokenize plus one thing: every pair of adjacent
// words, concatenated with no separator, added as an extra token. A real
// asset name is often a single compound word ("devicefarm-public-devices-
// fail-device-gate"), while a task descriptor phrases the same idea with a
// space ("Device Farm") - two entirely different token strings under plain
// tokenize, and zero overlap between "device"+"farm" and "devicefarm" no
// matter how relevant the match. Confirmed on a real corpus (issue #64):
// four real memory files with "devicefarm" in the name, zero of them
// matched a query containing "Device Farm". Computed from the raw word
// list before stopword filtering, since a genuine compound rarely has a
// stopword between its two halves anyway ("Device Farm", not "Device the
// Farm"). Used for matching only, never for the score's denominator - see
// score - so it can only add overlap, never take any away.
func tokenizeWithCompounds(s string) map[string]bool {
	out := tokenize(s)
	words := tokenPattern.FindAllString(strings.ToLower(s), -1)
	for i := 0; i+1 < len(words); i++ {
		out[words[i]+words[i+1]] = true
	}
	return out
}

// score is |queryMatchTokens ∩ candidateTokens| / |queryTokens|, how much of
// the task descriptor this asset's own words account for. Deliberately
// asymmetric (not Jaccard over the union): a short, precise skill description
// that is a subset of a long task descriptor should score as a strong match,
// not get penalized for being short.
//
// queryTokens (the denominator) and queryMatchTokens (the numerator side) are
// deliberately different sets: queryMatchTokens includes compound-word
// tokens (tokenizeWithCompounds) so a bonus match doesn't cost anything, but
// counting compounds in the denominator too would inflate it roughly as fast
// as any bonus overlap grows the numerator, erasing the benefit for exactly
// the long, natural-language queries it's meant to help (verified by hand
// against issue #64's own real query before shipping this shape).
func score(queryTokens, queryMatchTokens, candidate map[string]bool) float64 {
	if len(queryTokens) == 0 || len(candidate) == 0 {
		return 0
	}
	overlap := 0
	for w := range queryMatchTokens {
		if candidate[w] {
			overlap++
		}
	}
	return float64(overlap) / float64(len(queryTokens))
}

// Recommend scores every active asset in assets against desc and returns
// the top matches, plus a model/effort suggestion. It takes []ledger.AssetRow
// directly (not a *ledger.DB) so it stays testable without a database and
// callers control which assets are in scope (e.g. excluding stale ones).
//
// policy is the caller's already-resolved lookup for whatever agent type is
// about to run, or nil when none is known or none exists yet - Recommend
// itself has no database access, by the same design as the assets slice
// above. When non-nil, policy's own Model/Effort/Source/SampleSize drive the
// answer directly, real evidence measured on this machine's own runs, in
// place of the keyword-only cold-start guess below (issue #63: loom policy
// could show four agent types with evidence-backed rows, all landing on
// sonnet, while advise still reported "no history yet to personalize from" -
// the evidence existed, nothing consulted it).
func Recommend(desc TaskDescriptor, assets []ledger.AssetRow, policy *ledger.PolicyRow) Recommendation {
	query := tokenize(desc.Text)
	queryMatch := tokenizeWithCompounds(desc.Text)

	// Every candidate that shares anything at all, not just those clearing
	// minScore - the fallback below needs the full ranked list to draw a
	// weak-but-shown result from when nothing clears the normal bar.
	var all []SkillMatch
	for _, a := range assets {
		s := score(query, queryMatch, tokenizeWithCompounds(a.Name+" "+a.Description))
		if s <= 0 {
			continue
		}
		all = append(all, SkillMatch{
			Kind: a.Kind, Name: a.Name, Path: a.Path, Description: a.Description, Score: s,
			Suspicious: LooksSuspicious(a.Name + " " + a.Description),
		})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Score > all[j].Score })

	var matches []SkillMatch
	for _, m := range all {
		if m.Score >= minScore {
			matches = append(matches, m)
		}
	}
	// Never return empty when there was something, even weak, to show
	// (issue #64): a caller can discard a low-confidence suggestion, but
	// cannot discover an asset it was never told about. Only the top
	// scorers below threshold, not everything with any overlap at all -
	// still capped and still ranked, just candidly labelled as weak.
	belowThreshold := false
	if len(matches) == 0 && len(all) > 0 {
		belowThreshold = true
		matches = all
	}
	if len(matches) > maxMatches {
		matches = matches[:maxMatches]
	}

	model, effort, rationale := policyOrColdStart(desc.Text, policy)
	return Recommendation{Matches: matches, BelowThreshold: belowThreshold, Model: model, Effort: effort, Rationale: rationale}
}

// policyOrColdStart prefers policy, when given, over the keyword heuristic:
// a real, evidence-backed row measured on this machine's own runs is never
// worse advice than a guess from a handful of hand-picked words, regardless
// of how the policy got there (human-set or evidence-derived) - a decision
// someone already made for an agent type is not second-guessed here either
// way (see docs/design.md, "revertRegressedPolicies... not Loom's place to
// second-guess it with a number" - the same reasoning applies to advising
// against it).
func policyOrColdStart(text string, policy *ledger.PolicyRow) (model, effort, rationale string) {
	if policy != nil {
		effort := policy.Effort
		if effort == "" {
			effort = "medium"
		}
		if policy.Source == "evidence" {
			return policy.Model, effort, fmt.Sprintf(
				"%s has an evidence-backed policy: %s, measured over %d of your own runs - not a "+
					"cold-start guess (loom policy).", policy.AgentType, policy.Model, policy.SampleSize)
		}
		return policy.Model, effort, fmt.Sprintf(
			"%s has a policy you set by hand: %s (loom policy set). Not a cold-start guess.",
			policy.AgentType, policy.Model)
	}
	return coldStartModel(text)
}

// planningWords and retrievalWords are the two ends of the spectrum
// docs/design.md names explicitly under "Cold start": "cheaper model for
// well-scoped retrieval and mechanical execution, stronger model for planning
// and ambiguous synthesis." This is intentionally a short, hand-picked list,
// not a learned classifier, B2 has no run history to learn from yet, and a
// short list is one a user can read and correct.
//
// "why" deliberately removed (issue #63): it matched "investigate why the
// iOS Device Farm @full leg fails" - a debugging task, not a planning one -
// and pushed it to opus/high on that single word. "why" is common in
// ordinary investigation and debugging phrasing, not a reliable planning
// signal on its own.
var planningWords = tokenize("design architecture plan decide tradeoff strategy approach should evaluate review analyze")
var retrievalWords = tokenize("find search locate grep list where lookup rename bump update fix typo format")

// minPlanningHits is how many distinct planning-keyword hits it takes to
// escalate to the expensive tier - deliberately more than one (issue #63).
// On real measured data, opus costs roughly 272x sonnet's per-run cost, and
// the two ways to misjudge are not symmetric: guessing too cheap costs a
// retry, guessing too expensive costs the difference outright, every time,
// whether or not it turns out to have been warranted. A single incidental
// word is too weak a signal to justify that; two or more distinct planning
// words is a much more specific signal that the task is actually about
// design or synthesis rather than mentioning one word in passing.
const minPlanningHits = 2

// coldStartModel classifies free text into a model/effort suggestion using
// only word overlap against the two keyword sets above. Ties, and text
// matching neither list, fall through to the middle default, the honest
// answer when the heuristic has no opinion, not a forced guess. Weak
// evidence never escalates to the expensive tier (see minPlanningHits) -
// ambiguity defaults cheap, not expensive, because that is the direction a
// wrong guess costs less to correct in.
func coldStartModel(text string) (model, effort, rationale string) {
	words := tokenize(text)
	planningHits := countOverlap(words, planningWords)
	retrievalHits := countOverlap(words, retrievalWords)

	switch {
	case retrievalHits > 0 && retrievalHits >= planningHits:
		return "haiku", "low",
			"task descriptor matches retrieval/mechanical keywords, cold-start default favors a cheaper model for well-scoped work (docs/design.md, Cold start)"
	case planningHits >= minPlanningHits && planningHits > retrievalHits:
		return "opus", "high",
			"task descriptor matches multiple planning/synthesis keywords, cold-start default favors a stronger model for ambiguous work (docs/design.md, Cold start)"
	default:
		return "sonnet", "medium",
			"no strong signal either way, cold-start default favors the cheaper direction under ambiguity (a wrong guess costs a retry, not a blown budget)"
	}
}

func countOverlap(a, b map[string]bool) int {
	n := 0
	for w := range a {
		if b[w] {
			n++
		}
	}
	return n
}
