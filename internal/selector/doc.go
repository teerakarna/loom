// Package selector recommends which skill/agent/model/effort combination fits a
// task descriptor. Pure code, no model call: a transparent scoring function over
// referenced paths, tool verbs, skill description match, and similarity to past
// task descriptors and their measured outcomes. Deliberately not machine-learned
// — see docs/design.md ("Selector"). Not yet implemented.
package selector
