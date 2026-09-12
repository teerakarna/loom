package mcp

import (
	"context"
	"time"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teerakarna/loom/internal/ledger"
	"github.com/teerakarna/loom/internal/selector"
)

// version is the MCP server's own implementation version, independent of the
// loom binary's version — bump when a tool's input/output shape changes.
const version = "v0.2.0"

// NewServer builds the Loom MCP server backed by db. The caller owns db's
// lifecycle (open before, close after) — the server never opens or closes it
// itself, matching how internal/mcp is meant to be embedded by cmd/loom
// rather than manage its own state.
func NewServer(db *ledger.DB) *gomcp.Server {
	s := gomcp.NewServer(&gomcp.Implementation{Name: "loom", Version: version}, &gomcp.ServerOptions{
		Instructions: "Loom: local, read-only-to-the-cluster artifact lifecycle and cost/routing engine for Claude Code. " +
			"No network egress, no message content stored — see docs/design.md, 'Privacy by construction'.",
	})

	gomcp.AddTool(s, &gomcp.Tool{
		Name:        "query_ledger",
		Description: "Aggregate cost/usage report over every ingested Claude Code session and agent run, by model.",
	}, queryLedgerHandler(db))

	gomcp.AddTool(s, &gomcp.Tool{
		Name:        "get_recommendation",
		Description: "Given a free-text description of an upcoming task, recommend relevant existing skills/agents and a cold-start model/effort choice. Advisory only — never applies anything.",
	}, getRecommendationHandler(db))

	gomcp.AddTool(s, &gomcp.Tool{
		Name:        "list_proposals",
		Description: "List pending artifact promotion/retirement proposals with their evidence. Empty until Loom's proposal engine (B5) ships.",
	}, listProposalsHandler(db))

	gomcp.AddTool(s, &gomcp.Tool{
		Name:        "record_outcome",
		Description: "Record how a task turned out (accepted, corrected, or rejected), for future selector tuning. Write-only — never read back by this tool.",
	}, recordOutcomeHandler(db))

	return s
}

// LedgerReport mirrors ledger.Summary as the tool's JSON output shape —
// kept as its own type (rather than returning ledger.Summary directly) so
// the wire schema is documented at the boundary and doesn't silently change
// if ledger.Summary's internal shape does.
type LedgerReport struct {
	TotalRuns          int                 `json:"total_runs"`
	SessionRuns        int                 `json:"session_runs"`
	AgentRuns          int                 `json:"agent_runs"`
	TotalWeightedCost  float64             `json:"total_weighted_cost"`
	TotalToolUses      int                 `json:"total_tool_uses"`
	TotalDenials       int                 `json:"total_denials"`
	TotalFeedback      int                 `json:"total_feedback"`
	UnreconciledAgents int                 `json:"unreconciled_agents"`
	ByModel            []LedgerModelReport `json:"by_model"`
}

// LedgerModelReport is one row of LedgerReport.ByModel.
type LedgerModelReport struct {
	Model        string  `json:"model"`
	Runs         int     `json:"runs"`
	WeightedCost float64 `json:"weighted_cost"`
}

type emptyInput struct{}

func queryLedgerHandler(db *ledger.DB) gomcp.ToolHandlerFor[emptyInput, LedgerReport] {
	return func(_ context.Context, _ *gomcp.CallToolRequest, _ emptyInput) (*gomcp.CallToolResult, LedgerReport, error) {
		s, err := db.Report()
		if err != nil {
			return nil, LedgerReport{}, err
		}
		out := LedgerReport{
			TotalRuns: s.TotalRuns, SessionRuns: s.SessionRuns, AgentRuns: s.AgentRuns,
			TotalWeightedCost: s.TotalWeightedCost, TotalToolUses: s.TotalToolUses,
			TotalDenials: s.TotalDenials, TotalFeedback: s.TotalFeedback,
			UnreconciledAgents: s.UnreconciledAgents,
		}
		for _, mc := range s.ByModel {
			out.ByModel = append(out.ByModel, LedgerModelReport{Model: mc.Model, Runs: mc.Runs, WeightedCost: mc.WeightedCost})
		}
		return nil, out, nil
	}
}

// RecommendationInput is get_recommendation's argument: the same free text a
// user would type as a prompt.
type RecommendationInput struct {
	Text string `json:"text" jsonschema:"free-text description of the task about to be done"`
}

// RecommendationOutput is get_recommendation's result.
type RecommendationOutput struct {
	Matches   []SkillMatch `json:"matches"`
	Model     string       `json:"model"`
	Effort    string       `json:"effort"`
	Rationale string       `json:"rationale"`
}

// SkillMatch is one artifact scored against the task descriptor.
type SkillMatch struct {
	Kind        string  `json:"kind"`
	Name        string  `json:"name"`
	Path        string  `json:"path"`
	Description string  `json:"description"`
	Score       float64 `json:"score"`
}

func getRecommendationHandler(db *ledger.DB) gomcp.ToolHandlerFor[RecommendationInput, RecommendationOutput] {
	return func(_ context.Context, _ *gomcp.CallToolRequest, in RecommendationInput) (*gomcp.CallToolResult, RecommendationOutput, error) {
		artifacts, err := db.ListArtifacts()
		if err != nil {
			return nil, RecommendationOutput{}, err
		}
		active := activeOnly(artifacts)

		rec := selector.Recommend(selector.TaskDescriptor{Text: in.Text}, active)
		out := RecommendationOutput{Model: rec.Model, Effort: rec.Effort, Rationale: rec.Rationale}
		for _, m := range rec.Matches {
			out.Matches = append(out.Matches, SkillMatch{
				Kind: m.Kind, Name: m.Name, Path: m.Path, Description: m.Description, Score: m.Score,
			})
		}
		return nil, out, nil
	}
}

// activeOnly filters out stale artifacts before scoring — a skill or agent
// no longer on disk shouldn't be recommended, even if it's still in the
// ledger's history for later staleness reporting.
func activeOnly(rows []ledger.ArtifactRow) []ledger.ArtifactRow {
	var out []ledger.ArtifactRow
	for _, r := range rows {
		if r.Status == "active" {
			out = append(out, r)
		}
	}
	return out
}

// ProposalsOutput is list_proposals' result.
type ProposalsOutput struct {
	Proposals []Proposal `json:"proposals"`
}

// Proposal is one pending recommendation, as read from the ledger.
type Proposal struct {
	ID         int64    `json:"id"`
	Kind       string   `json:"kind"`
	Evidence   string   `json:"evidence"`
	SampleSize int      `json:"sample_size"`
	EffectSize *float64 `json:"effect_size,omitempty"`
	Status     string   `json:"status"`
	CreatedAt  string   `json:"created_at"`
}

func listProposalsHandler(db *ledger.DB) gomcp.ToolHandlerFor[emptyInput, ProposalsOutput] {
	return func(_ context.Context, _ *gomcp.CallToolRequest, _ emptyInput) (*gomcp.CallToolResult, ProposalsOutput, error) {
		rows, err := db.ListProposals()
		if err != nil {
			return nil, ProposalsOutput{}, err
		}
		out := ProposalsOutput{}
		for _, r := range rows {
			out.Proposals = append(out.Proposals, Proposal{
				ID: r.ID, Kind: r.Kind, Evidence: r.Evidence, SampleSize: r.SampleSize,
				EffectSize: r.EffectSize, Status: r.Status, CreatedAt: r.CreatedAt,
			})
		}
		return nil, out, nil
	}
}

// OutcomeInput is record_outcome's argument.
type OutcomeInput struct {
	TaskText string `json:"task_text" jsonschema:"the task description this outcome is about"`
	Outcome  string `json:"outcome" jsonschema:"one of: accepted, corrected, rejected"`
	Detail   string `json:"detail,omitempty" jsonschema:"optional free-text detail, e.g. what was corrected"`
}

// OutcomeOutput confirms the write. Kept minimal deliberately — this tool is
// write-only, per its own description.
type OutcomeOutput struct {
	Recorded bool `json:"recorded"`
}

func recordOutcomeHandler(db *ledger.DB) gomcp.ToolHandlerFor[OutcomeInput, OutcomeOutput] {
	return func(_ context.Context, _ *gomcp.CallToolRequest, in OutcomeInput) (*gomcp.CallToolResult, OutcomeOutput, error) {
		payload := map[string]string{"task_text": in.TaskText, "outcome": in.Outcome, "detail": in.Detail}
		if err := db.InsertEvent(time.Now(), "", "outcome", payload); err != nil {
			return nil, OutcomeOutput{}, err
		}
		return nil, OutcomeOutput{Recorded: true}, nil
	}
}
