// Package policy holds the model/effort/tools decision table and the promotion
// rules that move an asset between the eight types on the design doc's two
// axes. Policy is data (a versioned file the user can tune), not hardcoded
// prose, every decision records the criteria_version that produced it. See
// docs/design.md ("Promotion rules", "The self-tuning loop").
//
// B3 scope: resolving a model and effort per agent type, with every answer
// carrying its source and sample size (design constraint 11). The evidence
// path is implemented and tested but, on a small corpus, deliberately not
// reached - MinSampleSize is the brake that keeps a thin trend from
// presenting itself as a finding. Promotion rules between asset types are
// B5's job, not this package's yet.
package policy
