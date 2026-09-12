package selector

import "strings"

// injectionPhrases is a short, curated list of phrasing that shows up in
// real prompt-injection payloads: instructions addressed to an AI/assistant
// rather than to a human reader, embedded in content that is otherwise just
// a name or description. This is deliberately not exhaustive — see
// docs/design.md constraint 9: a phrase-based classifier is gameable by
// construction, and a longer list buys a false sense of security, not real
// protection. The point is a cheap, visible hint, not a filter.
var injectionPhrases = []string{
	"ignore previous instructions",
	"ignore all previous",
	"ignore the above",
	"disregard previous",
	"disregard all previous",
	"new instructions:",
	"system prompt",
	"you are now",
	"act as if",
	"do not tell the user",
	"do not inform the user",
	"assistant:",
	"</system>",
	"<system>",
}

// LooksSuspicious reports whether text contains phrasing typical of a
// prompt-injection attempt. Best-effort and advisory only — see SkillMatch.
// A false negative is expected and acceptable; this is one cheap signal
// among many a human might use, not a security control.
func LooksSuspicious(text string) bool {
	lower := strings.ToLower(text)
	for _, phrase := range injectionPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}
