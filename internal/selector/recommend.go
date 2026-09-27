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

// tokenizeWords lowercases s and splits it into its raw word list - the one
// pass every other tokenizer below builds from, so a caller needing more
// than one form of the same text (a plain unigram set and a compound set,
// say) never re-scans it. Found by code review: an earlier version fixed
// this for per-candidate tokenization but left the query itself scanned
// twice, once building the unigram set, once building the compound set.
func tokenizeWords(s string) []string {
	return tokenPattern.FindAllString(strings.ToLower(s), -1)
}

// unigramSet drops stopwords from words and returns what's left as a set -
// a set, not a slice, because scoring only cares which distinct words
// overlap, not how many times a word repeats.
func unigramSet(words []string) map[string]bool {
	out := map[string]bool{}
	for _, w := range words {
		if !stopwords[w] {
			out[w] = true
		}
	}
	return out
}

// tokenize is unigramSet over a single string - the common case for
// something that only needs one form of one string once (planningWords/
// retrievalWords at package init, coldStartModel's own text).
func tokenize(s string) map[string]bool {
	return unigramSet(tokenizeWords(s))
}

// compoundSet is unigramSet plus one thing: every pair of adjacent words,
// concatenated with no separator, added as an extra token. A real asset
// name is often a single compound word ("devicefarm-public-devices-fail-
// device-gate"), while a task descriptor phrases the same idea with a space
// ("Device Farm") - two entirely different token strings under plain
// unigrams, and zero overlap between "device"+"farm" and "devicefarm" no
// matter how relevant the match. Confirmed on a real corpus (issue #64):
// four real memory files with "devicefarm" in the name, zero of them
// matched a query containing "Device Farm". Compounds are built before
// stopword filtering, since a genuine compound rarely has a stopword
// between its two halves anyway ("Device Farm", not "Device the Farm").
// Used for matching only, never for the score's denominator - see score -
// so it can only add overlap, never take any away.
func compoundSet(words []string) map[string]bool {
	out := unigramSet(words)
	for i := 0; i+1 < len(words); i++ {
		out[words[i]+words[i+1]] = true
	}
	return out
}

// compoundsAcrossFields is compoundSet applied to each field independently,
// then merged - never across a field boundary. Found by code review,
// confirmed by reproduction: an earlier version tokenized a.Name+" "+
// a.Description as one string, so the last word of Name and the first word
// of Description could form a compound neither field actually contains -
// "mobile testing device" + "farm health checks..." spuriously produced
// "devicefarm" from two fields about unrelated things, inflating a real
// query's score (0.6 measured, 0.4 the true text overlap supports) purely
// from where Loom happened to join two strings before compounding.
func compoundsAcrossFields(fields ...string) map[string]bool {
	out := map[string]bool{}
	for _, f := range fields {
		for w := range compoundSet(tokenizeWords(f)) {
			out[w] = true
		}
	}
	return out
}

// score is |queryMatchTokens ∩ candidateTokens| / |queryTokens|, clamped to
// 1, how much of the task descriptor this asset's own words account for.
// Deliberately asymmetric (not Jaccard over the union): a short, precise
// skill description that is a subset of a long task descriptor should score
// as a strong match, not get penalized for being short.
//
// queryTokens (the denominator) and queryMatchTokens (the numerator side) are
// deliberately different sets: queryMatchTokens includes compound-word
// tokens (compoundSet) so a bonus match doesn't cost anything, but
// counting compounds in the denominator too would inflate it roughly as fast
// as any bonus overlap grows the numerator, erasing the benefit for exactly
// the long, natural-language queries it's meant to help (verified by hand
// against issue #64's own real query before shipping this shape).
//
// The clamp is load-bearing, not defensive dressing: queryMatchTokens can
// have more members than queryTokens (every compound is an extra token on
// top of the unigrams already counted), so when a candidate independently
// contains both a query's unigrams and their compound - a real case, not
// hypothetical: a candidate whose own description repeats the same phrase
// the query uses - overlap can exceed len(queryTokens) and the fraction
// would exceed 1 without this. Confirmed by reproduction before this
// shipped: "device farm" against a candidate whose own description also
// contains "Device Farm" scored 1.5. SkillMatch.Score's own doc comment
// promises 0 to 1; a caller (CLI printf, MCP JSON) treating an unclamped
// value as a confidence fraction would see a nonsensical result.
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
	s := float64(overlap) / float64(len(queryTokens))
	if s > 1 {
		return 1
	}
	return s
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
	queryWords := tokenizeWords(desc.Text)
	query := unigramSet(queryWords)
	queryMatch := compoundSet(queryWords)

	// Every candidate that shares anything at all, not just those clearing
	// minScore - the fallback below needs the full ranked list to draw a
	// weak-but-shown result from when nothing clears the normal bar.
	var all []SkillMatch
	for _, a := range assets {
		s := score(query, queryMatch, compoundsAcrossFields(a.Name, a.Description))
		if s <= 0 {
			continue
		}
		all = append(all, SkillMatch{
			Kind: a.Kind, Name: a.Name, Path: a.Path, Description: a.Description, Score: s,
			Suspicious: LooksSuspicious(a.Name + " " + a.Description),
		})
	}
	// Stable, not just sorted: score's clamp (see above) means two or more
	// candidates can land on the exact same score, and an unstable sort's
	// tie-break order is not guaranteed to be consistent across runs on
	// identical input - found by code review. Stable sort preserves assets'
	// own input order for ties, which is at least deterministic.
	sort.SliceStable(all, func(i, j int) bool { return all[i].Score > all[j].Score })

	// all is sorted descending, so the candidates clearing minScore are
	// always a contiguous prefix - find that cutoff instead of a second full
	// scan into a second slice (found by code review: once one element
	// fails the threshold, every later one does too, so the original loop
	// scanned to the end regardless).
	cutoff := 0
	for cutoff < len(all) && all[cutoff].Score >= minScore {
		cutoff++
	}
	matches := all[:cutoff]

	// Never return empty when there was something, even weak, to show
	// (issue #64): a caller can discard a low-confidence suggestion, but
	// cannot discover an asset it was never told about. Only the top
	// scorers below threshold, not everything with any overlap at all -
	// still capped and still ranked, just candidly labelled as weak.
	belowThreshold := false
	// A degenerate case within the degenerate case (issue #87): every
	// candidate tied at the exact same score, with more than one candidate
	// to tie, is not several weak guesses worth a caveat - it's the scorer
	// finding no real signal at all, the same shape a five-way tie across
	// entirely unrelated domains produces. A single candidate, or any spread
	// between the best and worst score, still gets #64's weak-but-real
	// guess; only the fully flat set is suppressed.
	if len(matches) == 0 && len(all) > 0 && (len(all) == 1 || all[0].Score != all[len(all)-1].Score) {
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
var retrievalWords = tokenize("find search locate grep list where lookup rename bump update typo format")

// debugWords is its own class, not folded into planningWords or
// retrievalWords (issue #87): "a bug of unknown cause is the opposite of
// well-scoped, and a wrong patch costs more than a slow one" - the same
// reasoning the planning class already gets, but "debug the parser and fix
// it" matches none of planningWords' vocabulary and used to match
// retrievalWords via "fix" alone, landing on the cheap tier for a task whose
// actual cause is unknown.
//
// "fix" moved here from retrievalWords - "fix a typo" is mechanical, but
// "fix" alone says nothing about whether the cause is known, and paired
// with "debug"/"investigate" it means the opposite of well-scoped.
//
// "why" deliberately left out, unlike the issue's own suggested list -
// tried it first, and it broke issue #63's own regression test:
// "investigate why the iOS Device Farm leg fails" then hit "investigate" and
// "why" as two distinct debugHits, clearing minDebugHits on what is one
// natural phrasing of a single act of investigating, not two independent
// signals. That is exactly the false escalation #63 removed "why" for in
// the first place, just reached through a second word this time. Left out
// rather than special-cased, so the two-distinct-signals rule minDebugHits
// exists to enforce stays true in practice, not just in name.
var debugWords = tokenize("debug diagnose fix investigate troubleshoot")

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

// minDebugHits mirrors minPlanningHits for the same reason: one incidental
// debug-flavoured word is too weak a signal on its own, but two or more is
// specific enough that the task is actually about an unknown cause rather
// than mentioning one debugging word in
// passing.
const minDebugHits = 2

// coldStartModel classifies free text into a model/effort suggestion using
// only word overlap against the two keyword sets above. Text matching
// neither list falls through to the middle default, the honest answer when
// the heuristic has no opinion, not a forced guess. Weak or conflicting
// evidence never escalates to the expensive tier (see minPlanningHits) -
// ambiguity defaults cheap, not expensive, because that is the direction a
// wrong guess costs less to correct in. A genuine tie between two strong
// signals (both sides clear their own bar) is conflicting evidence, not no
// evidence, and defaults cheap the same way, but the rationale says so
// honestly rather than borrowing the retrieval-dominant case's wording for
// a case that isn't that - found by code review, by reproduction: a text
// matching an equal number of planning and retrieval words got "matches
// retrieval/mechanical keywords" even though it matched just as many
// planning ones.
func coldStartModel(text string) (model, effort, rationale string) {
	words := tokenize(text)
	planningHits := countOverlap(words, planningWords)
	retrievalHits := countOverlap(words, retrievalWords)
	debugHits := countOverlap(words, debugWords)

	switch {
	// Checked before the retrieval and planning cases below: debugWords
	// shares no word with either set, but a task can still mention
	// unrelated retrieval or planning words alongside its debugging ones
	// ("debug and diagnose this, then find and list the results"), and
	// debug is checked first so it is not silently outranked by a
	// dominance test written for the other two classes (issue #87).
	//
	// This is ordering priority, not an escalate-on-any-tie rule: a genuine
	// tie between debugHits and retrievalHits (both clear their own bar)
	// falls through to the retrieval-tie case below and defaults cheap, the
	// same conflicting-evidence reasoning minPlanningHits' own comment
	// gives for the planning/retrieval tie - debug's own words carry no
	// more weight than planning's do at a real tie, and cost asymmetry
	// still means ambiguity should default cheap, not expensive. Found by
	// code review: an earlier version of this comment claimed debug "must
	// win that comparison" unconditionally, which the code never did and
	// should not do.
	case debugHits >= minDebugHits && planningHits >= minPlanningHits && debugHits == planningHits && debugHits > retrievalHits:
		return "opus", "high",
			"task descriptor matches debugging and planning/synthesis keywords equally, cold-start default favors a stronger model for either kind of ambiguous work (docs/design.md, Cold start)"
	case debugHits >= minDebugHits && debugHits > retrievalHits:
		return "opus", "high",
			"task descriptor matches multiple debugging keywords, cold-start default favors a stronger model since the cause is unknown and a wrong patch costs more than a slow one (docs/design.md, Cold start)"
	case retrievalHits > 0 && retrievalHits == planningHits:
		return "haiku", "low",
			"task descriptor matches planning and retrieval keywords equally, cold-start default favors the cheaper direction under conflicting signal (a wrong guess costs a retry, not a blown budget)"
	case retrievalHits > 0 && retrievalHits > planningHits:
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
