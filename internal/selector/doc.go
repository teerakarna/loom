// Package selector recommends which skill/agent/model/effort combination fits
// a task descriptor. Pure code, no model call: a transparent scoring function
// over word overlap between the descriptor and each discovered artifact's own
// name/description, plus a keyword-based model/effort heuristic for cold
// start. Deliberately not machine-learned — see docs/design.md ("Selector",
// "Cold start"): at realistic personal data volumes there is nothing to
// learn from, and a transparent function is debuggable, fast, and honest
// about its reasoning.
//
// B2 scope: recommendations only, advisory (docs/design.md phasing). It reads
// the artifacts and runs tables but never writes a policy — that's B3, once a
// policy table exists to write to.
package selector
