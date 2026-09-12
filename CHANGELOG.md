# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Runs now record `agent_type` and `effort` (B3a). Agent type is read from the `.meta.json`
  companion beside each subagent transcript, the only place it exists; effort is a top-level field
  on assistant lines. Both are optional and empty when absent. This is the key B3's per-agent-type
  policy needs, and it immediately showed a 30x per-run cost gap between `Explore` and `fork`
  agents on a real corpus.

### Fixed

- Weighted cost was overstated by 2.12x. One API response is written to the transcript as several
  JSONL lines, one per content block, each repeating the same `message.usage`, and ingest summed
  per line. Usage is now counted once per distinct `message.id`. Tool-use counts are deliberately
  not deduplicated, since each line carries genuinely distinct content blocks. Found by running
  `loom report` against a real corpus (#6); verified by re-ingesting a frozen 21-transcript
  snapshot and matching an independent implementation to the unit.

### Added

- CI: golangci-lint (`.golangci.yml`, standard linters), govulncheck, and gitleaks secret scanning
  as separate jobs alongside build/vet/test. Fixes 19 errcheck findings (unchecked `Close()` errors)
  surfaced by turning lint on for the first time.
- Dependabot config for `gomod` and `github-actions` dependency updates, weekly. Also enabled
  GitHub's Dependabot vulnerability alerts on the repo (a settings toggle, free on private repos —
  confirmed by testing, doesn't require going public).

- Prompt-injection hardening for the artifact-recommendation path: `get_recommendation` and
  `loom advise` re-serve name/description text read verbatim from local files, which is an
  indirect-injection surface once it lands back in another agent's context. Description text is
  now length-capped at discovery time, every affected MCP field and tool description says
  explicitly that the value is untrusted data and not an instruction, and a best-effort
  `suspicious` flag (never a filter) surfaces obviously injection-shaped phrasing. Documented as
  design doc constraint 9.

- Artifact discovery (`internal/artifact`): scans standard Claude Code locations plus the current
  project for skills, agents, plans, hooks, and per-project memory. Tolerant of both the flat
  `name.md` and directory-with-`SKILL.md` conventions, and of files with no frontmatter at all
  (falls back to the first `#` heading as a description).
- Selector (`internal/selector`): transparent word-overlap scoring of a free-text task descriptor
  against discovered artifacts, plus a keyword-based cold-start model/effort recommendation.
- MCP server over stdio (`internal/mcp`), exposing `query_ledger`, `get_recommendation`,
  `list_proposals`, and `record_outcome`.
- `loom advise <text>` and `loom serve` CLI commands.
- Ingest, ledger, `loom report` (B1).
- Project scaffolding and design documentation.
