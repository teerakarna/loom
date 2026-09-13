# Loom

An artifact lifecycle and cost/routing engine for [Claude Code](https://claude.com/claude-code).

Claude Code accumulates durable artifacts — scratch drafts, memory, skills, plans, hooks, agents,
workflows, plugins — and nothing tracks which are used, which have gone stale, what each one
costs, or which combination fits a given task. Loom reads the session transcripts Claude Code
already writes to disk, builds a local ledger of what actually happened, and reports where cost
and rework go. It measures and recommends; a human applies.

**Status: B1–B5 done, B6 in progress** — ingest, ledger and reporting; artifact discovery, selector
and MCP server; the policy table and generated agent definitions; per-lane filtering; advisor
proposals and the loop that re-measures after one is applied. See
[`docs/design.md`](docs/design.md) for the full design (problem, architecture, promotion rules,
phasing) and [`docs/transcript-schema.md`](docs/transcript-schema.md) for the transcript format
Loom ingests.

The evidence-driven half of the tool — pin proposals, retirement, regression detection — is
tested but has not yet fired on a real corpus. On real data it currently reports insufficient
evidence, which is correct and is also the point: see "Going public" in the design doc.

No content is ever stored — only derived metrics and identifiers — and there is no network
egress from the core. See the design doc's "Design constraints, non-negotiable" section.

## Install

```sh
go install github.com/teerakarna/loom/cmd/loom@latest
```

Optionally register the MCP server with Claude Code, so a session can query its own cost history:

```
/plugin marketplace add teerakarna/loom
/plugin install loom@loom
```

The plugin does not contain the binary — install it first. See [`plugin/README.md`](plugin/README.md).

## Usage

```sh
loom report                  # ingest ~/.claude/projects, print a cost/usage report
loom status                  # what Loom knows, how stale it is, and what it cannot answer
loom advise "task text"      # discover skills/agents/plans/hooks, recommend which are
                             # relevant plus a cold-start model/effort choice
loom propose                 # proposals the evidence supports, with what each rests on
loom policy                  # effective model/effort per agent type, with sample sizes
loom serve                   # run the MCP server on stdio
```

`loom serve` exposes five tools over MCP: `query_ledger`, `get_recommendation`, `list_proposals`,
`dismiss_proposal` and `record_outcome`.

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
