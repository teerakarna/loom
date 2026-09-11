// Package policy holds the model/effort/tools decision table and the promotion
// rules that move an artifact between the eight types on the design doc's two
// axes. Policy is data (a versioned file the user can tune), not hardcoded
// prose — every decision records the criteria_version that produced it. See
// docs/design.md ("Promotion rules", "The self-tuning loop"). Not yet
// implemented.
package policy
