# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Loop closure: applying a policy now records the baseline it was measured against, and later runs
  are compared against it. A regression surfaces as a `revert_policy` proposal carrying before/after
  evidence and saying what got worse; applying it restores the shipped default and re-opens the
  original question. Rework is weighed before cost, because a cheaper model that gets things wrong
  is not a saving.

### Fixed

- Stored timestamps are now always UTC. They were written with the local offset, but SQLite compares
  them as strings and RFC3339 only orders lexically when offsets match - so a run at `12:04Z` sorted
  *before* a policy written at `18:04+07:00` an hour earlier, and every comparison against a stored
  time was silently wrong off UTC. Found by running loop closure for real; every unit test passed
  because they all build times in UTC.

- `loom propose apply <id>` (B5d): takes a proposal that touches only Loom's own state, records the
  policy with its evidence and sample size, and tells you the one command that reverts it. Refuses
  anything touching your files, unconditionally. Auto-apply was rejected: a pin can fire at most
  once per agent type ever, so automation would have bought one saved command at the cost of
  preference storage, window caps and unattended writes. CLI-only, because an assistant can call an
  MCP tool without being asked.

- MCP proposal surface (B5b): `list_proposals` now refreshes from current ledger state before
  listing, so it never returns something stale because nobody ran the CLI, and carries
  `touches_user_files` - the field that says whether Loom may ever apply a proposal. Evidence is
  structured rather than an opaque JSON string, and each proposal carries a human-readable summary
  and rationale. New `dismiss_proposal` tool completes the loop in the place the user already is.

- `loom propose` (B5a): generates, stores and lists proposals the ledger's evidence actually
  supports. Two kinds so far - retiring an artifact unseen for 90 days, and pinning a model for an
  agent type with enough measured runs. Each says plainly whether Loom may apply it: anything
  touching the user's files never can, anything touching only Loom's own state reverts in one
  command. Dismissals hold until the evidence behind them changes rather than until an interval
  elapses, and the pending queue is capped.

- Runs are tagged with their lane: the project directory the session ran in, taken from the
  transcript path (B4). `loom report` gains a by-lane breakdown and a `--lane` filter. No manifest
  and no new file in any repo, because the lane is already in the path. Labelling by path is a
  description and claims nothing; inferring a *boundary* from a path would be a decision, which is
  the path-guessing the design doc forbids, so hard ledger separation stays deferred.

- `loom status`: what Loom knows, how current it is, and what it cannot answer (closes #21).
  Read-only by design, because `loom report` ingests as a side effect and checking freshness with it
  would change the thing being checked. Reports transcripts on disk vs ingested-and-current vs
  stale vs never-seen, which is the check that would have surfaced the ingest staleness bug
  immediately instead of hours later. Ends by listing what it does not track, rather than implying
  completeness it lacks.
- `loom report` now breaks the headline figure down by token class (closes #9). A bare nine-digit
  total hid the most useful fact about a corpus: on real data 76.3% of weighted cost is cache
  reads, the cheap class. The same total driven by fresh output would imply the opposite action.
  Weights are read from `internal/ingest` rather than duplicated, so the breakdown cannot drift
  from the figure it explains.

- `loom policy render` writes agent definitions carrying the pinned model into
  `~/.loom/generated/agents/` (B3d). Never installed for you and never written to a client's own
  agent directory (constraint 8). A decision resting on a shipped default is deliberately not
  rendered: a file restating a default is material to review with nothing new in it. Bounded,
  overwrites rather than accumulates, refuses to touch any file it did not generate, and rejects
  agent-type names that would escape the output directory.
- Policy resolution per agent type, and `loom policy` to show it (B3c). Every decision carries its
  source (`stored`, `evidence`, or `default`) and sample size, so a shipped guess can never be
  mistaken for a measured finding (constraint 11). The evidence path is implemented and tested but
  deliberately unreached below `MinSampleSize` (20 runs), which no agent type on a real corpus yet
  meets. `loom policy set`/`unset` provide the deliberate override and its one-command revert.
  Statistics use medians rather than means, because run cost is heavily skewed and a mean describes
  the outlier.
- `loom report` now shows per-run cost alongside totals, a cost-concentration summary, a by-agent-type
  breakdown, and the most expensive runs (B3b, closes #10). Totals alone conflate "costs more each
  time" with "used more often" and can invert the real ordering; on a real corpus one run turned out
  to be 71% of all measured cost, which no grouped view could show.
- Runs now record `agent_type` and `effort` (B3a). Agent type is read from the `.meta.json`
  companion beside each subagent transcript, the only place it exists; effort is a top-level field
  on assistant lines. Both are optional and empty when absent. This is the key B3's per-agent-type
  policy needs, and it immediately showed a 30x per-run cost gap between `Explore` and `fork`
  agents on a real corpus.

### Fixed

- Ingest re-reads a transcript whose size has changed, instead of skipping any path it has seen
  before. A session transcript grows continuously while the session runs, so any session ingested
  mid-flight was frozen at that moment permanently and no amount of re-running `loom report` would
  correct it. On a real corpus the largest run was understated by roughly half. `runs.size_bytes`
  records what was read, and `InsertRun` replaces rather than refusing, so "one file, one run"
  still holds.

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
