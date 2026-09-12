# Loom - an artifact lifecycle and routing engine for Claude Code

Status: design agreed 2026-09-11.
Licence: Apache-2.0. Repo: personal GitHub, private until v0.1.0, then public.
Target: full open source project, distributed via the Claude Code plugin marketplace.

**B1 done (2026-09-11):** ingest, ledger, `loom report` — working, tested, run against real
history. Two real findings along the way, both in `docs/transcript-schema.md`: the subagent
completion record is an XML-like `<usage>` block inside a `queue-operation` line's text content,
not JSON fields, and does not reconcile with a naive sum of the agent's own transcript; and
`message.model` can be the literal string `"<synthetic>"` (a locally-injected status/error line,
always zero usage) — both handled, tested, documented.

**B2 done (2026-09-12):** artifact discovery (skills, agents, plans, hooks, per-project memory —
`internal/artifact`), selector (`internal/selector`), MCP server over stdio exposing
`query_ledger`/`get_recommendation`/`list_proposals`/`record_outcome` (`internal/mcp`), and
`loom advise`/`loom serve` CLI commands. Two real findings running `loom advise` against this
machine's actual skills: several real skills have no YAML frontmatter at all (just a `#` heading) —
fixed with a first-heading fallback for the description signal; and naive word-overlap scoring
without stopword filtering produced false-positive matches on shared function words ("the", "a",
"and", "for") — fixed by filtering a stopword list before scoring. `list_proposals` correctly
returns empty — nothing writes to that table until B5.

B3 (policy table, generated agent definitions, model pinning) not started.

**Pick this up on a personal machine.** Everything below is generic by construction, with no
employer context in it. Build it on personal hardware, on personal time, under personal accounts,
and keep it that way: the codebase must contain nothing derived from any employer's data, which is
both an IP-hygiene position and a hard requirement for publishing at all.

---

## Problem

Claude Code accumulates durable artifacts: scratch drafts, memory, skills, plans, hooks, agents,
workflows, plugins. Nothing knows which of them are used, which have gone stale, what each one
costs, or which combination fits the task at hand. The predictable result is skills nobody invokes,
plans nobody finishes, hooks that fire every session for no reason, and model selection by guesswork.

Meanwhile the data to answer all of it already sits on every user's disk and nobody reads it.

## The enabling fact

Claude Code writes a transcript JSONL per session under `~/.claude/projects/<slugged-cwd>/`. Each
assistant turn carries:

- `message.model` - which model actually served the turn
- `message.usage` - `input_tokens`, `cache_read_input_tokens`, `cache_creation_input_tokens`,
  `output_tokens`, plus service tier
- `effort` - reasoning effort
- `timestamp`, `durationMs`
- every `tool_use` block with its name and full input
- `toolDenialKind` where a tool call was denied
- `userFeedback` where the user corrected something
- `isSidechain` to distinguish delegated turns

Subagent runs get their own files at `<session-id>/subagents/agent-<id>.jsonl`, and task-completion
notifications in the parent transcript record `subagent_tokens`, `tool_uses` and `duration_ms`,
which gives an independent figure to reconcile computed costs against.

So the measurement layer requires **no instrumentation at all**. Ingest is retroactive over whatever
history a user already has, then follows. This is what makes the tool useful on the day it is
installed, and it is why hooks are an optional extra rather than the foundation.

## What Loom does

Builds a local ledger of what you actually do. Reports where cost and rework go. Recommends which
combination of features to reach for on a given task. Proposes artifact promotions and retirements as
reviewable diffs.

It measures and recommends. The human applies. That split is deliberate and permanent for anything
with blast radius.

---

## Design constraints, non-negotiable

The tool ships to strangers whose setups look nothing like the author's. Every one of these exists
to stop the design collapsing into one person's habits.

1. **Useful at n=0.** A new user with no history benefits immediately. Ship defaults reasoned from
   first principles; personalise as evidence accrues. Never require a corpus to be useful.
2. **Discover, never assume.** Find artifacts by scanning the standard Claude Code locations and the
   current project. No required layout, no required naming convention, no assumption that any given
   artifact type is even in use.
3. **Schema-tolerant.** Read whatever frontmatter exists, treat missing fields as unknown, never
   rewrite a user's file to fit a preferred schema. Several skill frontmatter conventions exist in
   the wild; that is fine and not Loom's business to unify.
4. **No deployment-tool assumptions.** No dotfiles manager, no particular shell, no config
   framework. Install via `go install`, a Homebrew tap, a release binary, or the plugin.
5. **Boundaries are optional.** Default is one scope and zero configuration. Separation (work vs
   personal, or per client) is opt-in for those who need it, never a premise.
6. **Privacy by construction.** The ledger stores derived metrics and identifiers only, never
   message content. No network egress from the core, as an invariant. This is the property that
   makes it safe to point at transcripts containing confidential work.
7. **Degrade, never block.** If the engine is absent, stopped or broken, every integration point
   returns cleanly and the session proceeds unaffected.
8. **Never write to human-authored files.** Loom writes to its own database, its own generated-output
   directory, and proposal files. Nothing else. Settings files are read-only, always. A tool in this
   space once shipped a version that deleted users' hand-written hook entries; that class of failure
   gets designed out, not carefully avoided.
9. **Artifact-derived text is data, never instructions.** A skill/agent/plan's name, description, or
   heading is read off disk and re-served verbatim through `get_recommendation` into whatever session
   called it — that is untrusted content re-entering another agent's context, the same shape of
   problem as Candor's "all ingested telemetry is untrusted" rule. Loom cannot force a downstream
   client to treat it as data rather than commands, but it bounds the blast radius: every such field
   is length-capped, and every tool that returns one says so in its own schema. A cheap heuristic
   flag on obviously injection-shaped text (`suspicious`) is surfaced alongside a match — advisory
   only, per constraint 1's whole ethos, never a filter. Full detection is not attempted: phrase-based
   classifiers are gameable and a false sense of security is worse than an honest gap.
10. **Every accelerator ships with its brake, and nothing grows without a bound.** Any mechanism
   that can act, spend, or generate has its limit defined and enforced in the same change that
   introduces it, never in a later phase. A brake added afterwards is not a brake: the window it
   was missing is exactly the window the thing ran unattended.

   This bites hardest for a tool whose entire purpose is reducing accumulated material. A mechanism
   that answers "too many artifacts to keep track of" by producing more artifacts has made the
   problem worse while appearing to help. So anything Loom generates states its retention rule up
   front: how many, for how long, overwritten or appended.

   Concretely, and these are current gaps as of 2026-09-12, not solved problems:
   - `record_outcome` appends to `events` with no cap. Needs a bound, or a documented reason the
     unbounded growth is acceptable.
   - B5's proposals need a generation limit and dedupe. An advisor producing proposals faster than
     a human accepts them recreates exactly the fatigue this tool exists to reduce.
   - The pre-compact hook (see "Integration") writes a resume pointer. It must **overwrite**, one
     per session, never append, or a context-relief mechanism becomes a context-consuming one.

11. **Say which numbers are evidence and which are assumption.** A figure derived from one run must
   not render identically to one derived from fifty. Recommendations carry their sample size, and
   the selector states when it is using a shipped default rather than measured history (see "Cold
   start"). The same discipline applies to anything the ledger reports: an unreconciled figure, a
   default, and a measured median are three different kinds of claim and are labelled as such.

   You can reason from a model of the world that is not yet backed by data, and often you have to.
   What is not allowed is losing track of which is which, because every downstream decision inherits
   that confusion silently.


---

## The model

Eight artifact types on two axes. The axes are what make the promotion rules principled rather than
a matter of taste.

**Axis 1, decision authority at trigger time.** Who decides, and how binding it is:

```
Scratch  -> Memory -> Skill -> Plan -> Agent -> Hook -> Workflow
(ad hoc)    (fact)   (proc)   (intent) (delegated) (deterministic) (code)
```

`Plugin` is not a peer on this axis. It is packaging over any combination of the others.

**Axis 2, cost shape.** Three sub-costs, kept separate because they fail differently:

- **Tokens.** Context, and any LLM-backed hook, which pays a model call on every fire. Agents
  dominate, and model choice there is the only lever with roughly a 5x spread.
- **Wall clock and network.** Any command hook doing I/O, paid on every trigger, almost always
  unmeasured.
- **Blast radius.** Hooks that deny or rewrite tool calls. Cheap in tokens, expensive when wrong.

### Promotion rules

Shipped as a versioned data file the user can tune, not hardcoded prose. Every decision records the
`criteria_version` that produced it.

| From | To | Criteria |
|---|---|---|
| Scratch | Plan | Records intent plus decisions with a defined end state, not merely a record of a conversation |
| Scratch | Skill | The *shape* recurs (handover, status update, escalation) independent of its subject |
| Context | Memory | A fact, not a procedure; useful in a future session; needs no judgement to apply |
| Memory | Skill (procedure) | Has steps, needed twice or more, and you can write a description that genuinely discriminates when to invoke it. If the trigger phrase will not come, it is not ready |
| Memory | Skill (reference) | Domain-scoped rather than project-scoped. Memory loads only for its own project, so a cross-project fact needs a skill to be reachable everywhere. This is why factual skills with no steps are legitimate, and why "a skill must be a procedure" is the wrong rule |
| Skill | Agent | Delegable end to end with a bounded tool set, and costly enough that pinning a cheaper model than the orchestrator pays for itself |
| Skill | Hook | The trigger is structurally detectable (tool name, regex, glob) and a probabilistic miss is expensive. If judgement cannot reduce to a pattern but the rule must always fire, use an LLM-backed hook and accept the per-fire cost. Do not force a bad regex |
| Plan | Workflow | Steps repeat across a list of similar items, each independently resumable, and the shape held stable across two or more real executions |
| Workflow | Skill (reverse) | Once the orchestration shape itself is reusable, wrap it in a skill whose only job is when to reach for it |
| Any set | Plugin | One cohesive concern, needs independent enable/disable/version, or bundles an MCP server |

**The single most useful rule:** a deterministic hook requires a structurally checkable condition.
If you cannot write the condition, it is not a hook. Unconditional hooks that fire every session
just to print a reminder are the common anti-pattern this catches, and they are pure cost.

### Scratch

Loosely defined on purpose. A scratch area is wherever someone keeps ad hoc, on-the-fly drafts:
messages to colleagues, meeting notes, half-formed thinking. Usually untracked, usually disposable.

Loom treats it as a **read-only inbox**. No imposed naming convention, no template, no directory
structure, and it never writes there. Its only interest is spotting when something has outgrown
scratch: a draft that is really a plan, or a message shape written for the fourth time that should be
a skill. Path is configurable, discovery is best-effort, and the whole feature is skippable.

### Lanes

A lane is a session workspace whose *declared* scope governs, rather than whichever directory the
session happened to start in. Optional, off by default.

The useful trick is launching a session in a dedicated, otherwise empty directory, which makes
CLAUDE.md inheritance and project-memory keying deliberate instead of incidental. Without it, memory
stores fragment across whatever paths sessions were started from.

A lane is a directory plus a manifest:

```toml
name       = "example-lane"
scope      = "scoped"           # pinned | scoped | themed | open
paths      = ["../some-repo"]   # what this lane may touch
topics     = ["deploys", "ci"]
promote_to = "../some-repo/plans"
boundary   = "default"          # opt-in separation
```

The scope dial, tight to loose:

- `pinned` - one repo, writes only there
- `scoped` - a named set of repos
- `themed` - topic tags; writes outside the lane need confirmation
- `open` - scratch; no writes outside the lane directory

`boundary` is the optional separation primitive: one ledger per boundary, never joined, taken from
the manifest and never guessed from a path. Path-guessing is exactly how content ends up in the wrong
store, so the manifest is the only source of truth.

**Coordination between lanes** uses append-only ledger tables (`messages`, `presence` with a TTL,
`tasks`) plus a human-readable view rendered from them. The failure mode this replaces is a shared
markdown coordination file that grows without bound and drifts from whatever the docs claim it does.

---

## Architecture

Three layers. Nearly all logic sits in layer 1, so the parts wired into a client stay small and cheap.

### 1. Core engine - Go, single static binary

Go for stdlib coverage, cross-compilation to four platforms from one machine, and a single binary
with no runtime to install. macOS and Linux at v0.1.0.

**Ingest.** Streaming JSONL reader with per-file byte-offset checkpoints, decoding line by line so
memory stays bounded regardless of corpus size. Retroactive on first run, then follows changes via
fsnotify, watching *directories* rather than individual files to stay clear of platform FD limits,
debounced, with a slow reconcile sweep as the safety net for missed events. Event-driven rather than
polling: real-time reaction at near-zero idle cost.

**Ledger.** SQLite, WAL mode. Six tables:

- `artifacts` - one row per known artifact instance, with type, path, status
- `events` - append-only; this is the ledger
- `runs` - one row per session or agent execution, with weighted cost and outcome
- `policies` - the model/effort/tools decision table
- `proposals` - pending recommendations with their evidence
- `coordination` - messages, presence, tasks

No content store and no full-text index. Recall of past conversation is a different problem and
explicitly not this tool's job. Expected size is single-digit megabytes.

**Selector.** Given a task descriptor, returns a recommended combination: which skills to surface,
which agent type at which model and effort, whether an existing workflow already covers it. Pure
code, no model call, and a transparent scoring function over a handful of features (referenced
paths, tool verbs, skill description match, similarity to previous task descriptors and their
measured outcomes). Deliberately **not** machine-learned: at realistic personal data volumes there
is nothing to learn from, and a transparent function is debuggable, fast, and honest about its
reasoning.

**Serve.** Local only. Unix domain socket at mode 0600 for the fast path, so an integration point
costs a sub-millisecond round trip rather than opening the database. No TCP, no network egress.
Callers fall back to a one-shot read when the daemon is not running.

### 2. Integration - MCP first

**An MCP server over stdio is the primary surface.** It works in any MCP client, needs no edits to
anyone's settings file, and installs in one line. Tools exposed: query the ledger, get a
recommendation for the task at hand, list and explain proposals, record an outcome.

**Hooks are optional enhancements**, documented but never required, because on some setups the
settings file is owned by another process and hook registration is a permanent manual step. Users who
want them get five, none LLM-backed, each with a stated latency budget:

| Hook | Job | Budget |
|---|---|---|
| session start | Lane briefing plus opening recommendation | < 20ms |
| prompt submit | Per-task recommendation | < 20ms |
| stop / subagent stop | Prompt cost attribution ahead of transcript flush | < 5ms |
| pre-compact | Write a resume pointer: table of contents plus runnable queries, never content | < 50ms |

**Note on pre-compact (2026-09-12):** this hook is a pointer-writer, not a gatekeeper, and that's a
permanent design decision, not a placeholder to revisit. It pulls from the ledger — most expensive
runs this session, denied/corrected tool calls, artifacts touched — and writes that as the resume
pointer; `session start` reads it back as the briefing. It never decides what Claude Code's own
compaction keeps or drops. Same reasoning as "It measures and recommends. The human applies." above:
a hook that prunes or rewrites context has the same blast-radius shape as a hook that denies or
rewrites tool calls (Axis 2, cost shape) — cheap-if-wrong stays the only kind of hook Loom ships.
Not yet built — B2 shipped selector + MCP server only, no hooks.

**CLI** for humans: `loom init`, `report`, `advise`, `policy`, `lane`, `serve`.

**Agent definitions are generated output.** Model pinning lives in agent frontmatter, rendered from
the `policies` table, so the policy is data and the files are a reviewable diff. Generated files land
in their own directory and never overwrite hand-written ones.

**One skill**, the Advisor, which reads proposals and walks the user through accepting or rejecting
them. A diff-and-decide pattern applied to artifacts.

### 3. Distribution

- `go install`, a Homebrew tap, and release binaries for darwin/arm64, darwin/amd64, linux/amd64,
  linux/arm64
- **A Claude Code plugin** for the marketplace. Plugins are cloned content and cannot carry a
  compiled binary, so the plugin ships the MCP server registration, the optional hooks, the Advisor
  skill and a manifest; its install step fetches the matching platform binary and verifies it,
  refusing an unverified one rather than falling back
- Release integrity: goreleaser, SHA256 checksums, cosign keyless signing, SBOM per release. A tool
  that downloads a binary is a supply chain and should behave like one

---

## The self-tuning loop

Measure, propose, apply, re-measure. One human gate, at apply.

**Measured, all from data already on disk:**

- Weighted cost per run. Token kinds are not interchangeable: cached input is roughly an order of
  magnitude cheaper than fresh input, so the cost function weights by kind. Summing raw tokens would
  make every downstream recommendation wrong. Get this right first or nothing else means anything.
- Per `{agent type, model, effort}`: median tokens, duration, tool-call count.
- Rework proxies: tool denials, user corrections following a result, repeated near-identical tool
  calls, plan churn.
- Artifact use counts and staleness.

**Cold start.** With no history the selector uses shipped defaults reasoned from first principles
(cheaper model for well-scoped retrieval and mechanical execution, stronger model for planning and
ambiguous synthesis) and says that is what it is doing. As runs accumulate, evidence displaces
defaults per agent type, each recommendation carrying its sample size. Personalisation earns its way
in rather than being assumed. This is the mechanism by which the tool moulds to a user instead of
imposing a shape on them.

**Proposals** are diffs against the policy table, each carrying sample size, effect size, and a
pointer back to the runs that justify it. Applying writes a marker so the next measurement window
compares against the previous one. That comparison is what makes this a loop rather than a report.

**Guardrails, because samples are small and quality regressions are quiet:**

- No proposal below a minimum sample size. Report effect size, never a bare mean.
- Never propose a cheaper model for an agent type whose measured rework rate is already elevated.
- Every applied change reverts to the prior policy version in one command.

---

## Phasing

Each stage is independently useful. Stopping after any of them leaves something that works.

- **B1** Ingest, ledger, `loom report`. Read-only and retroactive. Answers "where does my spend
  actually go" from existing history on day one. This is the stage that proves the whole premise.
- **B2** Selector plus the MCP server. Recommendations only, advisory.
- **B3** Policy table, generated agent definitions, model pinning. The cost payoff.
- **B4** Lanes and ledger-backed coordination.
- **B5** Advisor proposals for promotion and retirement.
- **B6** Plugin packaging, signed releases, public at v0.1.0.

## Verification

- **Ingest correctness.** Replay fixtures and assert computed subagent totals reconcile with the
  totals the transcripts themselves record. If they do not reconcile, the cost model is wrong and
  nothing downstream can be trusted.
- **Memory bound.** Ingest a large synthetic corpus under a hard RSS ceiling, proving streaming.
- **Latency.** Socket round trip under 20ms warm. Kill the daemon mid-session and confirm every
  integration point still returns and the session is unaffected.
- **Write containment.** Run against a read-only fixture tree; assert zero writes outside the
  database and generated-output directory, and assert settings files are never opened for write.
- **Boundary isolation.** Point one boundary's ingest at a fixture belonging to another and assert
  refusal rather than absorption.
- **No content stored.** Ingest a fixture containing a planted secret, then grep the database for it.
  It must not appear.
- **Cold start.** Run every command against an empty ledger; assert useful output, no crashes, no
  silent no-ops.
- **Loop closure.** Apply a policy change, run a second window, confirm the delta is attributed and
  that revert restores the prior policy.

## Publishability rules

The tool's only input is transcripts, which on any real machine contain confidential material. So:

- Every fixture is **synthetic and hand-written**. No fixture may be derived from a real transcript,
  even redacted: redaction of a large corpus is not verifiable, invention is.
- Docs, README and examples use invented names only. No real hostnames, repo names, ticket
  references or organisation names.
- The generic mechanisms are publishable. The *instances* (which boundaries exist, which repos, which
  skills) are user configuration and never enter the repo.
- CI runs secret scanning plus a fixture-provenance check on every PR, and both are green before the
  repository is made public. **Secret scanning done (2026-09-12):** gitleaks, as a CI job, not
  GitHub's native secret-scanning toggle — that feature requires the repo to already be public (or
  paid GitHub Advanced Security), confirmed by trying to enable it via the API while private and
  getting "not available for this repository." Relying on it would have made this rule impossible to
  satisfy in the intended order. Fixture-provenance check not yet built.

## Project scaffolding

`LICENSE` (Apache-2.0), `NOTICE`, `README.md`, `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`,
`SECURITY.md` with a reporting route, issue and PR templates, `CHANGELOG.md`, semver tags, GitHub
Actions only for test, lint, build and release.

Layout: `cmd/loom/`, `internal/ingest/`, `internal/ledger/`, `internal/selector/`,
`internal/policy/`, `internal/mcp/`, `plugin/`, `testdata/`, `docs/`.

## First session on the personal machine

1. Create the repo, private, Apache-2.0, with the scaffolding above.
2. Write the transcript schema notes into `docs/` first: the JSONL field list at the top of this
   document is the contract everything else depends on, and it is worth pinning down and testing
   against synthetic fixtures before any other code exists.
3. Build B1 only. Ingest, ledger, `report`. Point it at that machine's own history and see whether
   the weighted cost figures reconcile against the recorded `subagent_tokens` values.
4. Stop there and look at the report before designing anything further. If B1's numbers are not
   trustworthy or not interesting, the rest of the design does not deserve to be built.
