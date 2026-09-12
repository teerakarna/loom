# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

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
