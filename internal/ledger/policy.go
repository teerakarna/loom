package ledger

import (
	"database/sql"
	"errors"
	"sort"
	"time"
)

// PolicyRow is one deliberate policy: an agent type someone (or sufficient
// evidence) has decided a model and effort for. A type with no row has no
// policy and falls back to a shipped default. See the schema comment in
// ledger.go for why defaults are never seeded here.
type PolicyRow struct {
	CriteriaVersion string
	AgentType       string
	Model           string
	Effort          string
	Source          string // "human" | "evidence"
	SampleSize      int    // runs behind an "evidence" row; 0 for "human"
	// Baseline* is what this policy was measured against when applied. Zero
	// for a hand-set policy, which has no measured baseline to regress from.
	// CreatedAt doubles as the marker time: runs after it are the comparison
	// window (see StatsByAgentTypeSince).
	BaselineMedianCost   float64
	BaselineDenialRate   float64
	BaselineFeedbackRate float64
	CreatedAt            string
}

// UpsertPolicy writes one policy, replacing any existing row for that agent
// type. One row per agent type, so the table is bounded by the number of
// agent types a user actually runs, not by how often policy is revisited
// (constraint 10).
func (d *DB) UpsertPolicy(p PolicyRow, at time.Time) error {
	_, err := d.sql.Exec(`
		INSERT INTO policies (criteria_version, agent_type, model, effort, source, sample_size,
			baseline_median_cost, baseline_denial_rate, baseline_feedback_rate, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_type) DO UPDATE SET
			criteria_version = excluded.criteria_version,
			model = excluded.model,
			effort = excluded.effort,
			source = excluded.source,
			sample_size = excluded.sample_size,
			baseline_median_cost = excluded.baseline_median_cost,
			baseline_denial_rate = excluded.baseline_denial_rate,
			baseline_feedback_rate = excluded.baseline_feedback_rate,
			created_at = excluded.created_at`,
		p.CriteriaVersion, p.AgentType, p.Model, p.Effort, p.Source, p.SampleSize,
		p.BaselineMedianCost, p.BaselineDenialRate, p.BaselineFeedbackRate, formatTime(at))
	return err
}

// GetPolicy returns the stored policy for agentType, or nil if there is none.
// Nil is the common case and is not an error: most agent types will never have
// a deliberate policy set.
func (d *DB) GetPolicy(agentType string) (*PolicyRow, error) {
	var p PolicyRow
	err := d.sql.QueryRow(`
		SELECT criteria_version, agent_type, model, COALESCE(effort, ''), source, sample_size,
		       baseline_median_cost, baseline_denial_rate, baseline_feedback_rate, created_at
		FROM policies WHERE agent_type = ?`, agentType).
		Scan(&p.CriteriaVersion, &p.AgentType, &p.Model, &p.Effort, &p.Source, &p.SampleSize,
			&p.BaselineMedianCost, &p.BaselineDenialRate, &p.BaselineFeedbackRate, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// DeletePolicy removes the stored policy for agentType, returning resolution
// to the shipped default. This is the revert path the design doc requires:
// "every applied change reverts to the prior policy version in one command".
func (d *DB) DeletePolicy(agentType string) error {
	_, err := d.sql.Exec(`DELETE FROM policies WHERE agent_type = ?`, agentType)
	return err
}

// AgentTypeStats is the measured evidence for one agent type. Medians, not
// means: run costs are heavily skewed (one run was 71% of a real corpus), and
// a mean over that describes the outlier rather than the typical run.
type AgentTypeStats struct {
	AgentType     string
	Runs          int
	MedianCost    float64
	MedianTools   float64
	DenialRate    float64 // denials per run
	FeedbackRate  float64 // user corrections per run
	ObservedModel string  // most common model seen for this agent type
}

// StatsByAgentTypeSince is StatsByAgentType restricted to runs that started
// after t. This is the comparison window for loop closure: runs before the
// marker justified the policy, runs after it are the test of whether that was
// right. Measured identically to the baseline so the two are like for like.
func (d *DB) StatsByAgentTypeSince(agentType string, t time.Time) (AgentTypeStats, error) {
	all, err := d.statsByAgentType(` AND agent_type = ? AND started_at > ?`, agentType, formatTime(t))
	if err != nil {
		return AgentTypeStats{}, err
	}
	if len(all) == 0 {
		return AgentTypeStats{AgentType: agentType}, nil
	}
	return all[0], nil
}

// StatsByAgentType computes evidence per agent type across all agent runs.
// Agent runs with no attributable type (no readable .meta.json) are excluded
// rather than grouped under "", since a bucket of unattributable runs is not
// an agent type and must never be mistaken for one.
func (d *DB) StatsByAgentType() ([]AgentTypeStats, error) {
	return d.statsByAgentType("")
}

// statsByAgentType is the shared implementation. extra is appended to the
// WHERE clause so a filtered window and the whole-corpus view cannot drift in
// how they measure.
func (d *DB) statsByAgentType(extra string, args ...any) ([]AgentTypeStats, error) {
	rows, err := d.sql.Query(`
		SELECT agent_type, weighted_cost, tool_use_count, denial_count, feedback_count, COALESCE(model, '')
		FROM runs WHERE kind = 'agent' AND agent_type != ''`+extra+` ORDER BY agent_type`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	type acc struct {
		costs, tools   []float64
		denials, backs int
		modelCounts    map[string]int
		agentType      string
	}
	byType := map[string]*acc{}
	var order []string

	for rows.Next() {
		var at, model string
		var cost float64
		var tools, denials, backs int
		if err := rows.Scan(&at, &cost, &tools, &denials, &backs, &model); err != nil {
			return nil, err
		}
		a, ok := byType[at]
		if !ok {
			a = &acc{modelCounts: map[string]int{}, agentType: at}
			byType[at] = a
			order = append(order, at)
		}
		a.costs = append(a.costs, cost)
		a.tools = append(a.tools, float64(tools))
		a.denials += denials
		a.backs += backs
		if model != "" {
			a.modelCounts[model]++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]AgentTypeStats, 0, len(order))
	for _, at := range order {
		a := byType[at]
		n := len(a.costs)
		s := AgentTypeStats{
			AgentType:     at,
			Runs:          n,
			MedianCost:    median(a.costs),
			MedianTools:   median(a.tools),
			ObservedModel: mostCommon(a.modelCounts),
		}
		if n > 0 {
			s.DenialRate = float64(a.denials) / float64(n)
			s.FeedbackRate = float64(a.backs) / float64(n)
		}
		out = append(out, s)
	}
	return out, nil
}

// median returns the middle value of vs, or 0 for an empty slice. Sorts a copy
// so the caller's slice is untouched.
func median(vs []float64) float64 {
	if len(vs) == 0 {
		return 0
	}
	c := append([]float64(nil), vs...)
	sort.Float64s(c)
	mid := len(c) / 2
	if len(c)%2 == 1 {
		return c[mid]
	}
	return (c[mid-1] + c[mid]) / 2
}

// mostCommon returns the key with the highest count, breaking ties by name so
// the result is deterministic rather than dependent on map iteration order.
func mostCommon(counts map[string]int) string {
	best, bestN := "", 0
	for k, n := range counts {
		if n > bestN || (n == bestN && k < best) {
			best, bestN = k, n
		}
	}
	return best
}
