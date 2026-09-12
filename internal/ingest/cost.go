package ingest

// Weights approximate the real relative price ratios Anthropic publishes for
// input / cached-input / cache-write / output tokens. This is a heuristic
// proxy for "relative cost" in weighted-token units, not a real-currency
// figure — Loom has no maintained per-model dollar price table (that would
// need updating every time pricing changes, and the design doc's own point
// is that *relative* cost is what drives useful recommendations, not exact
// dollars). See docs/design.md, "The self-tuning loop": "cached input is
// roughly an order of magnitude cheaper than fresh input, so the cost
// function weights by kind. Summing raw tokens would make every downstream
// recommendation wrong."
// Exported so a report can break a total down by token class using the same
// numbers the cost function uses. Two copies of these weights would drift, and
// a breakdown that does not sum to its own headline is worse than no breakdown.
const (
	WeightInput      = 1.0
	WeightOutput     = 5.0
	WeightCacheRead  = 0.1
	WeightCacheWrite = 1.25
)

const (
	weightInput        = WeightInput
	weightOutput       = WeightOutput
	weightCacheRead    = WeightCacheRead
	weightCacheWrite1h = WeightCacheWrite
	weightCacheWrite5m = WeightCacheWrite
)

// WeightedCost returns a relative cost figure for one Usage — sum tokens by
// kind, not raw total tokens, or every downstream comparison is wrong.
func WeightedCost(u Usage) float64 {
	return float64(u.InputTokens)*weightInput +
		float64(u.OutputTokens)*weightOutput +
		float64(u.CacheReadInputTokens)*weightCacheRead +
		float64(u.CacheCreationEphemeral1hTok)*weightCacheWrite1h +
		float64(u.CacheCreationEphemeral5mTok)*weightCacheWrite5m
}

// Add returns the element-wise sum of two Usage values.
func (u Usage) Add(o Usage) Usage {
	return Usage{
		InputTokens:                 u.InputTokens + o.InputTokens,
		OutputTokens:                u.OutputTokens + o.OutputTokens,
		CacheReadInputTokens:        u.CacheReadInputTokens + o.CacheReadInputTokens,
		CacheCreationInputTokens:    u.CacheCreationInputTokens + o.CacheCreationInputTokens,
		CacheCreationEphemeral1hTok: u.CacheCreationEphemeral1hTok + o.CacheCreationEphemeral1hTok,
		CacheCreationEphemeral5mTok: u.CacheCreationEphemeral5mTok + o.CacheCreationEphemeral5mTok,
	}
}
