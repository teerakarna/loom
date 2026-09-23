package selector

import (
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
	Matches   []SkillMatch
	Model     string
	Effort    string
	Rationale string
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

// score is |queryTokens ∩ candidateTokens| / |queryTokens|, how much of the
// task descriptor this asset's own words account for. Deliberately
// asymmetric (not Jaccard over the union): a short, precise skill description
// that is a subset of a long task descriptor should score as a strong match,
// not get penalized for being short.
func score(query, candidate map[string]bool) float64 {
	if len(query) == 0 || len(candidate) == 0 {
		return 0
	}
	overlap := 0
	for w := range query {
		if candidate[w] {
			overlap++
		}
	}
	return float64(overlap) / float64(len(query))
}

// Recommend scores every active asset in assets against desc and
// returns the top matches, plus a cold-start model/effort suggestion. It
// takes []ledger.AssetRow directly (not a *ledger.DB) so it stays testable
// without a database and callers control which assets are in scope (e.g.
// excluding stale ones).
func Recommend(desc TaskDescriptor, assets []ledger.AssetRow) Recommendation {
	query := tokenize(desc.Text)

	var matches []SkillMatch
	for _, a := range assets {
		s := score(query, tokenize(a.Name+" "+a.Description))
		if s < minScore {
			continue
		}
		matches = append(matches, SkillMatch{
			Kind: a.Kind, Name: a.Name, Path: a.Path, Description: a.Description, Score: s,
			Suspicious: LooksSuspicious(a.Name + " " + a.Description),
		})
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Score > matches[j].Score })
	if len(matches) > maxMatches {
		matches = matches[:maxMatches]
	}

	model, effort, rationale := coldStartModel(desc.Text)
	return Recommendation{Matches: matches, Model: model, Effort: effort, Rationale: rationale}
}

// planningWords and retrievalWords are the two ends of the spectrum
// docs/design.md names explicitly under "Cold start": "cheaper model for
// well-scoped retrieval and mechanical execution, stronger model for planning
// and ambiguous synthesis." This is intentionally a short, hand-picked list,
// not a learned classifier, B2 has no run history to learn from yet, and a
// short list is one a user can read and correct.
var planningWords = tokenize("design architecture plan decide tradeoff strategy approach why should evaluate review analyze")
var retrievalWords = tokenize("find search locate grep list where lookup rename bump update fix typo format")

// coldStartModel classifies free text into a model/effort suggestion using
// only word overlap against the two keyword sets above. Ties, and text
// matching neither list, fall through to the middle default, the honest
// answer when the heuristic has no opinion, not a forced guess.
func coldStartModel(text string) (model, effort, rationale string) {
	words := tokenize(text)
	planningHits := countOverlap(words, planningWords)
	retrievalHits := countOverlap(words, retrievalWords)

	switch {
	case planningHits > retrievalHits:
		return "opus", "high",
			"task descriptor matches planning/synthesis keywords, cold-start default favors a stronger model for ambiguous work (docs/design.md, Cold start)"
	case retrievalHits > planningHits:
		return "haiku", "low",
			"task descriptor matches retrieval/mechanical keywords, cold-start default favors a cheaper model for well-scoped work (docs/design.md, Cold start)"
	default:
		return "sonnet", "medium",
			"no strong signal either way, cold-start default (no history yet to personalize from)"
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
