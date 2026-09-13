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

**B3 done (2026-09-12):** policy table, model pinning, generated agent definitions. Shipped in
four slices:

- **B3a** — `runs` now records `agent_type` and `effort`. Neither existed, so per-agent-type policy
  had nothing to key on. `agentType` lives only in the `.meta.json` companion beside each subagent
  transcript; `effort` is top level on assistant lines, not inside `message`. This closed a standing
  open item in `docs/transcript-schema.md`.
- **B3b** — `loom report` gained per-run cost, cost concentration, a by-agent-type breakdown, and
  the most expensive runs (closes #10). Totals alone invert the real ordering when run counts
  differ, which the policy engine would otherwise have inherited.
- **B3c** — policy resolution per agent type: stored, then evidence meeting `MinSampleSize` (20),
  then a shipped default. Every `Decision` carries `Source` and `SampleSize`, so constraint 11 is
  enforced by type rather than convention. `loom policy` shows it; `set`/`unset` give the
  deliberate override and its one-command revert.
- **B3d** — `loom policy render` writes agent definitions into Loom's own directory, never a
  client's. A decision resting on a shipped default is not rendered at all.

**The evidence path is deliberately unreached.** No agent type on a real corpus meets the 20-run
threshold (Explore has 14, fork has 4), so everything correctly resolves to a labelled default and
says what is still needed. That is the agreed behaviour, not an unfinished implementation.

Two findings from dogfooding, both fixed: usage was summed per transcript line when one API
response spans several lines, overstating cost by 2.12x (#6); and `UpsertPolicy` failed on any
ledger predating the `UNIQUE` constraint on `agent_type`, because SQLite cannot add a constraint
via `ALTER TABLE` and every unit test built its table fresh.

Issue #9 (the headline figure is uninterpretable and hides that 75% of weighted cost is cache
reads) was moved to B4 rather than held against B3: it is report presentation, not correctness, and
nothing in B3 depends on it. The B3 milestone is closed.

**B4 done (2026-09-13): lanes.** Runs are tagged with the project directory their session ran in,
taken from the transcript path. `loom report` gained a by-lane breakdown and a `--lane` filter, and
`loom status` reports lanes seen plus how many runs could not be placed in one.

Notable for what it did *not* ship. B4 arrived as a six-field manifest with a four-position
write-enforcement dial and left as one column, one filter and a display helper:

- **Write enforcement cut.** `scope`, `paths` and `promote_to` all policed what a session may touch.
  Loom has no hook into writes, constraint 9 forbids it writing to human-authored files, and
  enforcing would need a blocking `PreToolUse` hook - the blast-radius category, and a
  contradiction of "measures and recommends, the human applies".
- **The manifest cut.** The lane is already in the transcript path, so labelling needs no declared
  input. Labelling by path is a *description* and claims nothing; inferring a boundary would be a
  *decision*, which is the path-guessing this doc forbids by name.
- **Hard ledger separation deferred.** It is the only part that genuinely needs a manifest, and
  filtering one local ledger turned out to be enough. Constraint 12, not separation, is what makes
  the ledger safe on a machine mixing personal and work lanes - a correction recorded in the Lanes
  section rather than quietly fixed.

Also shipped alongside: `loom status` (#21), a cost-by-token-class breakdown so the headline figure
explains itself (#9), and `loom init` cut from the CLI list (#22) because it had no job the tool did
not already do.

B5 (advisor proposals for promotion and retirement) not started. Ledger-backed coordination was
**cut on 2026-09-12** rather than deferred:
it breaks constraint 7 by making sessions depend on Loom being installed, and it invents a mechanism
rather than optimising an existing one. Full reasoning under "Coordination between lanes: rejected".

Loom never reads or writes any external coordination state. Artifact discovery scans skills, agents,
plans and per-project memory only, so a file-based exchange convention elsewhere in `~/.claude/` is
neither ingested nor interfered with, and its content never reaches the ledger (constraint 6).

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

12. **The ledger is machine-local, and stays that way.** It is never synced, committed, backed up to
   a shared location, or checked into a dotfiles manager. A rule, not an accident of where the file
   currently happens to sit.

   Constraint 6 keeps message content out. It does not keep *identifiers* out, and on any corpus
   that matters those identify real work: full transcript paths encode project directory names, and
   discovered artifacts carry their names and descriptions. Keeping the ledger on the machine that
   produced it is what makes Loom safe to point at a sensitive corpus at all.

   Corollary for anything published — docs, examples, issues, screenshots, write-ups: it comes only
   from a corpus you are willing to publish from. Loom cannot know which of a user's machines that
   is, and does not guess. The discipline belongs to whoever is publishing.

   This is deliberately *not* a work-versus-personal rule. Whether a user draws that distinction at
   all is theirs to decide (constraint 5); this says only that wherever a ledger is written, it
   stays there.

> **A note on these numbers.** Constraints are cited by number from Go comments, CONTRIBUTING.md and
> commit messages. Append new ones; never insert into the middle, which silently invalidates every
> existing reference.


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

**Rescoped 2026-09-13** against how lanes are actually used, rather than how this doc originally
imagined them. The original spec is preserved at the end of this section, because the gap between
the two is the useful part.

A lane is a directory you launch a session from, with its purpose defined by convention and memory.
That looseness is deliberate: in real use any lane may review a PR from any repo, so lanes overlap
constantly, and the overlap is a feature rather than a problem to design out.

**Overlap and separation happen at different levels, which is what makes this tractable:**

- A *session* belongs to exactly one lane: the directory it was launched from. Unambiguous.
- A *lane* may associate with any number of repos and topics, and a repo may belong to many lanes.
  Overlap is free, because associations are metadata and nothing is partitioned by them.
- A *run* is attributed to its session's lane, never to the repos it touched. So the ambiguity of
  "which lane owns this repo" never has to be answered.

#### Filtering, not a manifest

**Decided 2026-09-13: B4 ships no manifest.** A run's project directory is already in its transcript
path, so tagging and filtering by it costs nothing and needs no configuration at all.

What a manifest would add, and why none of it justifies the file yet:

| Want | Needs a manifest? |
|---|---|
| Tag runs by project directory, filter reports by it | **No** - already in the path |
| A friendly lane name instead of a slugged path | Yes, but purely cosmetic |
| Hard ledger separation between boundaries | **Yes** - a decision, not inferable |

Hard separation is the only one that genuinely requires declared input, because a boundary is a
*decision* rather than a *description*, and inferring a decision from a path is the path-guessing
this doc forbids by name. Labelling by path is fine: it describes where a session ran, and claims
nothing.

**A correction worth keeping.** An earlier version of this section argued that `boundary` was what
made Loom "safely installable" on a machine mixing personal and work lanes. That was wrong.
Constraint 12 is what makes it safe: the ledger stays on the machine that produced it. Mixing lanes
in one local ledger on a machine that already holds both is an *analysis* limitation, not a safety
one. You lose the ability to ask what work cost versus personal. Useful to have, not load-bearing,
and the difference matters when deciding what to build.

So the separation dial reduces to two positions today, with the third available later if a real need
appears:

| Strength | Mechanism | Status |
|---|---|---|
| None | one ledger, no lane recorded | today |
| Soft | one ledger, runs tagged by project directory, filterable | **B4** |
| Hard | one ledger per declared boundary | deferred; needs a manifest, no evidenced need yet |

#### Stated limitation

A run is attributed to the directory its session ran in, never to the repos it touched. Reviewing a
work PR from a personal lane files that run under the personal lane. That is a description of where
the work happened, which is true, rather than a claim about what the work was about, which would not
be.

#### What was cut from the original spec, and why

The original manifest carried a `scope` dial: `pinned` (writes only to one repo), `scoped`,
`themed` (writes outside the lane need confirmation), `open`. Every one of those is **write
enforcement**, and Loom cannot do it. It has no hook into what a session writes, constraint 9
forbids it writing to human-authored files, and enforcing would require a blocking `PreToolUse`
hook — the blast-radius category, and a direct contradiction of "it measures and recommends, the
human applies".

`promote_to` and `paths` went with it: both existed to serve enforcement.

What replaced the dial is a dial about *data separation*, which Loom genuinely owns. Same idea of
"a range of strength", applied to something the tool can actually deliver.

<details>
<summary>Original lane spec, superseded</summary>

```toml
name       = "example-lane"
scope      = "scoped"           # pinned | scoped | themed | open
paths      = ["../some-repo"]   # what this lane may touch
topics     = ["deploys", "ci"]
promote_to = "../some-repo/plans"
boundary   = "default"          # opt-in separation
```

Scope dial, tight to loose: `pinned` one repo writes only there; `scoped` a named set of repos;
`themed` topic tags with writes outside needing confirmation; `open` scratch with no writes outside
the lane directory.

</details>

**Coordination between lanes: rejected 2026-09-12.** This originally specified append-only ledger
tables (`messages`, `presence` with a TTL, `tasks`) to replace a shared markdown coordination file.
It is not being built, and the reasoning is worth keeping rather than leaving as silence.

**It breaks constraint 7.** "If the engine is absent, stopped or broken, every integration point
returns cleanly and the session proceeds unaffected." Measurement degrades gracefully: no Loom, no
report, session unaffected. Coordination cannot. If sessions coordinate through Loom's SQLite and
Loom is not installed, they cannot see each other at all. That is a hard dependency, not a
degradation, and it is the one category of feature this constraint structurally forbids.

**It breaks the thesis.** The enabling fact above is that the data already exists and the
measurement layer needs *no instrumentation at all*. Coordination is the opposite: it invents a
mechanism Claude Code does not have and requires every session to opt into a protocol. That is a
category shift from optimising what exists to adding something new.

**It imposes one person's habits**, which the design constraints open by forbidding. A file-based
convention in someone's own home directory is inert to everyone else. Coordination tables shipped
in Loom push that opinion onto every user of the tool.

**Files are the more enduring choice here.** Every durable Claude Code artifact is a file: skills,
memory, plans, CLAUDE.md, transcripts. A markdown/JSONL convention runs with the grain of the
platform, is greppable, debuggable without a CLI, and survives Loom being uninstalled or rewritten.
A table in one tool's private database does none of that.

**And it complements nothing.** Lanes feed the selector: declared scope informs recommendations.
Coordination would share a database with the rest of Loom and nothing else. A separate product
wearing the same binary.

The `coordination` table stays in the schema as an empty, unused artifact of the original design
rather than being dropped in a migration, but nothing reads or writes it. Anyone wanting
cross-session coordination should use a file convention outside Loom.

**Correction, same day.** An earlier version of this section argued the rejection partly on the
grounds that the author's own file-based exchange had "gone unused". That was wrong: it was inferred
from one machine, where the exchange happens to be quiet, and generalised to a practice spanning
several environments. On the author's work machine the exchange is in active, heavy use. The
rejection stands on the five arguments above, none of which depend on usage, and the usage argument
is withdrawn rather than quietly deleted.

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
- `coordination` - unused. Retained empty; see "Coordination between lanes: rejected" above

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

**CLI** for humans: `report`, `status`, `advise`, `policy`, `serve`, and `lane` once B4 lands.

`loom init` was listed here and is **cut (2026-09-13)**, not deferred. It was never implemented and
belonged to no phase, and on inspection it had no job: `ledger.Open` creates and migrates the
database on first use, and `report`/`advise` discover and ingest with no setup step. A command whose
only function is to do nothing visible is worse than its absence, because it implies a setup ritual
that does not exist. If a real need appears later, a lane manifest scaffold is the likeliest
candidate and belongs to B4.

`loom status` was added in its place, answering the question a new user actually has: is this
working, what does it know, and how current is it. It is read-only by design, because `report`
ingests as a side effect and a user checking freshness with `report` would be changing the thing
they were checking.

**Agent definitions are generated output.** Model pinning lives in agent frontmatter, rendered from
the `policies` table, so the policy is data and the files are a reviewable diff. Generated files land
in their own directory and never overwrite hand-written ones.

**One skill**, the Advisor, which reads proposals and walks the user through accepting or rejecting
them. A diff-and-decide pattern applied to artifacts.

### 3. Distribution

- `go install`, a Homebrew tap, and release binaries for darwin/arm64, darwin/amd64, linux/amd64,
  linux/arm64
- **A Claude Code plugin** for the marketplace. Plugins are cloned content and cannot carry a
  compiled binary, so the plugin ships the MCP server registration and a manifest, and resolves a
  binary the user installed themselves.

  This paragraph used to end "its install step fetches the matching platform binary and verifies it,
  refusing an unverified one rather than falling back", and listed hooks and an Advisor skill among
  the contents. All three were wrong by the time B6c was built: the hooks and the skill had been
  cut, and verifying a download needs signatures to verify against, which B6a has not shipped. Left
  here as a correction rather than edited away, because a doc that quietly rewrites its own promises
  is exactly what this project keeps telling other people not to do.
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

### B5 scope, agreed 2026-09-13

#### Two kinds of proposal, and only one may ever be automated

The split falls straight out of constraint 9 and decides everything else:

| A proposal that touches | Loom may | Automation |
|---|---|---|
| **The user's files** - promote a scratch note to a plan, retire an unused skill | render a diff, nothing more | **Never.** Not once, not with permission. Loom does not write there at all |
| **Loom's own state** - pin a model in the `policies` table | apply it | Defensible: blast radius is confined to Loom's ledger, and `loom policy unset` already reverts it in one command |

"It measures and recommends. The human applies" stays absolute for anything outside Loom's own
database. Inside it, automation is a convenience over an action the user could already take, and it
is revertible.

#### Surfacing, in the order it earns

Loom has no terminal of its own. It is a CLI and an MCP server; Claude Code owns the keyboard and
the screen. There is no hotkey, no submenu and no TUI - a Loom that owned the terminal would be
competing with the thing it exists to observe. Three surfaces exist, and each earns the next:

1. **Pull.** `loom propose` lists pending proposals with their evidence. You ask, it answers, it
   exits. Zero intrusion, no new machinery.
2. **The MCP tool.** `list_proposals` already exists and returns empty. This is the most natural
   notification available: the user is already in a conversation, so a proposal can be raised when
   it is relevant rather than when a timer fires.
3. ~~**Session-start hook, optional.**~~ **Cut 2026-09-13**, on the evidence below. It was the third
   surface and it does not earn its place.

   - **It blows its own latency budget by 48x.** The table above budgets session-start hooks at
     under 20ms. Measured: 10ms warm, **960ms cold**. Session start is precisely when the binary is
     cold, so the realistic figure is the bad one.
   - **It would have nothing to say for months.** On a real corpus: the nearest retire proposal is
     90 days away *and* requires a skill to actually be deleted; the nearest pin needs 6 more runs
     of one agent type and 16 of the other. So the honest output at every session start, for weeks,
     is "0 proposals" - a line that costs context and says nothing.
   - **It would write at session start.** Generating proposals mutates the ledger. A hook that
     writes before the user has typed anything is a side effect in the wrong place, and a failed
     write there is worse than a failed read.
   - **A read-only version is circular.** Making it cheap means counting already-stored proposals
     without generating. But proposals only exist if something already generated them, so on any
     machine where the CLI has not been run it reports 0 forever. It would tell you about proposals
     that exist only if you already looked.
   - **The slot is contested.** A skill-maintenance hook already occupies SessionStart, and the
     lane briefing in the table above wants it too. Three claimants on one line of startup context.

   **`list_proposals` already does this better.** It surfaces a proposal when it is relevant to what
   the user is doing, rather than unconditionally at startup, which is better targeting at zero
   fixed cost. Push was the wrong instinct; pull-when-relevant is the right one.

   This is the failure constraint 10 names, caught before building rather than after: a mechanism
   that answers "too much to keep track of" by printing more at every session start has made the
   problem worse.

#### Automation controls live in the ledger, not a config file

`apply once` / `ask every time` / `apply every time` / `edit the value` is the right shape, and the
preference belongs in the ledger alongside `policies`, not in a new dotfile. Same reasoning that
cut the lane manifest: do not add a file when there is already somewhere to put it, and a tool whose
default is zero configuration should stay that way.

#### Bounds, per constraint 10, in the same change that adds the mechanism

- A cap on **pending** proposals. An advisor producing proposals faster than a human accepts them
  recreates exactly the fatigue this project exists to reduce.
- **Dedupe**: the same finding is not re-proposed on every run. A dismissed proposal stays dismissed
  until the evidence behind it changes, which is the same content-addressed idea as the fingerprint
  gate in Candor: re-raise on change, not on a timer.
- A cap on **auto-applies per window**, with every one recorded and revertible, for the Loom-internal
  kind only.

#### Build order

Pull first, MCP tool second, ~~session-start nudge third~~ (cut, see above), auto-apply last and
only for Loom-internal state. Each step is useful stopping there, and nothing later is required to
make anything earlier worth having.

**Auto-apply: rejected 2026-09-13, replaced by a one-command manual apply.**

The arithmetic kills it. A pin proposal is suppressed once a policy exists, so it can fire **at most
once per agent type, ever** - two times on the corpus this was measured against - and each firing
saves exactly one command. That does not justify preference storage (`apply once` / `ask every
time` / `apply every time`), a cap on auto-applies per window, and writes happening while nobody is
watching.

What the convenience was actually worth is not retyping a model name off a proposal. So
`loom propose apply <id>` does that, and nothing else: a person runs it, it writes only to Loom's
own policy table, it records the policy's source as `evidence` with its sample size, and
`loom policy unset` reverts it. "The human applies" stays literal rather than becoming a setting.

Apply is deliberately **CLI-only**. `list_proposals` and `dismiss_proposal` are exposed over MCP
because listing is read-only and dismissing only hides a suggestion, but applying changes policy,
and an assistant can call an MCP tool without the human asking. The terminal is where a decision
that changes state belongs.

Status: pull shipped (B5a), MCP surface shipped (B5b), nudge cut, auto-apply rejected and replaced
by `loom propose apply` (B5d). **B5 complete as scoped.**

### Loop closure, built 2026-09-13

Applying a policy currently ends the story. The doc above already says it should not: *"Applying
writes a marker so the next measurement window compares against the previous one. That comparison is
what makes this a loop rather than a report."* That marker does not exist, so the self-tuning loop
is currently a one-way trip.

**The asymmetry that makes this work.** Auto-*apply* was rejected, but auto-*revert* is a different
proposition and a more defensible one: applying moves you to an unproven state, whereas reverting
restores a known-good state you were already in and had evidence for. So the position is
**apply manually, verify automatically, revert automatically when the evidence turns** - which is
coherent in a way "apply automatically" never was.

What it needs:

- **A marker on apply.** `Apply` records what changed, when, and the baseline it was justified by
  (the median cost, tool count and rework rates behind the proposal). Attached to *apply*, not to
  auto-apply: a policy change needs verifying whoever made it, and manual apply is what exists.
- **A comparison window.** Runs for that agent type *after* the marker, measured the same way, so
  before and after are like for like.
- **A regression threshold**, stated rather than implied, and a minimum post-apply sample so a
  regression cannot be declared on two runs (constraint 11 applies to this judgement as much as to
  the original proposal).
- **A revert path that says why**, not merely that. "Reverted Explore to the shipped default: median
  cost rose 40% over 22 runs since the pin" is actionable; "reverted" is not.

**Honest limitation, stated up front.** This needs enough runs *after* an apply to mean anything.
With a 20-run threshold to propose in the first place, a comparable window afterwards is not quick.
It ships correct and quiet, like the evidence path in B3 - intended behaviour, not a shortfall.

**Built as a third proposal kind**, `revert_policy`, so it reuses the machinery that already exists
rather than adding a parallel one. A regression becomes a proposal with before/after evidence;
applying it removes the policy; and because the policy is gone, the original pin becomes proposable
again. The loop closes *and* re-opens, which is what makes it a loop.

**Automatic revert was considered and is not needed**, for a reason that only became clear once the
nudge was cut: **nothing triggers Loom unattended.** There is no daemon and no hook. "Auto-revert"
would mean "revert the next time you run `loom propose`" - at which point you are looking at the
proposal anyway, and applying it is one command. The automation would buy nothing that the surface
does not already give.

Thresholds, stated rather than implied: `MinPostApplyRuns` (10, deliberately below the 20 needed to
*apply* - the bar for returning to a known-good state is lower than for leaving it),
`RegressionCostRatio` (1.25), `RegressionReworkDelta` (0.2 per run). Rework is checked before cost,
because a cheaper model that gets things wrong is not a saving.

---

## Phasing

Each stage is independently useful. Stopping after any of them leaves something that works.

- **B1** Ingest, ledger, `loom report`. Read-only and retroactive. Answers "where does my spend
  actually go" from existing history on day one. This is the stage that proves the whole premise.
- **B2** Selector plus the MCP server. Recommendations only, advisory.
- **B3** Policy table, generated agent definitions, model pinning. The cost payoff.
- **B4** Lanes, rescoped 2026-09-13 down to what needs no configuration: tag each run with the
  project directory its session ran in, and let reports and policy filter by it. No manifest, no
  new file in anyone's repo. Write enforcement was cut (Loom cannot do it without becoming a
  gatekeeper) and hard ledger separation was deferred (it needs declared input, and filtering a
  single local ledger turned out to be enough).
- **B5** Advisor proposals for promotion and retirement.
- **B6** Signed releases, plugin packaging, and the decision to go public. Scoped in detail below.

### B6 scope, agreed 2026-09-13

Four pieces, and they are not equally justified. Two are clearly needed, one is thinner than this
doc has been claiming, and one is a decision rather than a build.

#### B6a. Signed releases - needed, and coupled to publishing

goreleaser, SHA256 checksums, cosign keyless signing, SBOM per release, binaries for darwin/arm64,
darwin/amd64, linux/amd64, linux/arm64. None of it exists today: there is no `.goreleaser.yml` and
no release workflow.

A tool that asks people to download a binary is a supply chain and should behave like one. Note the
coupling though: nobody downloads a binary until there is a public release, so this is worth
building *as part of* going public rather than before deciding to.

#### B6b. Fixture-hygiene check - BUILT 2026-09-13

The publishability rules require this in CI before the repo goes public. It is worth building and
worth being honest about: **provenance cannot be verified from content.** Nothing can prove a file
was hand-written rather than derived from a real transcript.

What it can do is check for the *markers* of real data, which is a different and weaker claim that
should be made in those words:

- no absolute home paths (`/Users/<name>`, `/home/<name>`) - currently clean
- no email addresses, no bearer tokens (gitleaks already covers credentials)
- a size ceiling, since real transcripts run to megabytes and every current fixture is under 2.5KB
- a manifest in `testdata/README.md` naming each fixture and asserting it was hand-written, so the
  claim is at least recorded and reviewable rather than assumed

Naming it provenance would be the tool claiming more than its evidence supports, which this project
keeps telling other people not to do - so it is called hygiene.

Built as a Go test (`internal/ingest/fixtures_test.go`) rather than a separate CI job, so it runs
wherever `go test` does with no new machinery. Each check was verified to fail against a planted
violation rather than assumed to work: an undeclared fixture, a `/Users/<name>` path, a
600-character unbroken string, and a 60KB file were each caught.

#### B6c. The Claude Code plugin - BUILT 2026-09-13, thinner than this doc said

The plugin was specified as shipping "the MCP server registration, the optional hooks, the Advisor
skill and a manifest". Two of those four no longer exist:

- **The hooks.** The session-start nudge was cut on measurement, and no other hook was ever built.
  There are no hooks to ship.
- **The Advisor skill.** Described as reading proposals and walking the user through accepting or
  rejecting them. `list_proposals`, `dismiss_proposal` and `loom propose apply` already do exactly
  that, through surfaces that exist. A skill wrapping them would be a third way to do the same
  thing.

So the plugin would ship an MCP server registration and a manifest. That is not nothing - a one-line
install beats "edit your settings.json by hand", and marketplace presence is the distribution
channel. But it should be built as what it is, and the plugin README updated, rather than shipping
against a description that no longer matches.

**Built to that narrower description.** `plugin/.claude-plugin/plugin.json`, `plugin/.mcp.json`, and
a `.claude-plugin/marketplace.json` at the repo root so the repository is its own marketplace. The
README carries a table of what ships and what does not, including the two pieces that were specified
and then cut.

Three things came out of building it that the scope above did not anticipate:

- **The MCP command is a wrapper script, not `"command": "loom"`.** Claude Code launches an MCP
  server with the environment it was itself launched with. `go install` puts the binary in
  `~/go/bin`, which is on an interactive shell's PATH and is not on the PATH a desktop app inherits
  from the launcher - verified, not assumed. Registering the bare name works from a terminal and
  fails from the app, which is the worst available failure mode because it reads as a Loom bug.
  `plugin/bin/loom-mcp` checks `$LOOM_BIN`, then PATH, then where the documented install methods
  actually put it, and says so in words when it finds nothing.

- **The plugin does not fetch or verify a binary**, which is what "Distribution" below said it
  would. That needs signed releases to verify against and there are none (B6a). A fetch step with
  no signature to check would be the shape of supply-chain safety without the substance. It
  resolves a binary the user installed and is explicit that this is what it does.

- **`claude plugin validate --strict` exists**, is first-party, runs unauthenticated, and is now a
  CI job. It checks schema shape only: a manifest pointing at a command that does not exist, a
  wrapper that has lost its executable bit, and a version that disagrees with the server being
  shipped all passed validation while broken. Each was planted and confirmed to pass, which is why
  `internal/mcp/plugin_test.go` covers those three and the validator covers the schema. The two are
  complementary rather than redundant.

#### B6d. Going public - a decision, with prerequisites

Not a build. The prerequisites are already written down in the publishability rules:

- secret scanning green in CI - **done** (gitleaks, 2026-09-12)
- fixture-hygiene check green - **done 2026-09-13**
- a manual pass for real names in docs, README and examples - currently clean, checked 2026-09-13,
  and worth re-checking immediately before rather than trusting this line
- signed releases actually working - **B6a**
- **at least one evidence path has fired on real data.** Not a build, a precondition. Every
  evidence-driven behaviour in this tool - pin proposals, retirement, regression detection - has so
  far only run against synthetic fixtures and seeded demos. On a real corpus they all correctly
  report insufficient evidence, which is right, and which means the interesting half of the tool is
  unproven outside its own tests. Publishing something whose headline behaviour has never been
  demonstrated on real data would be the thing this project spends four blog posts criticising.

  This gate costs nothing to add and is satisfied by patience: keep it installed, let the corpus
  grow past a threshold, and watch one proposal appear for a real reason. The thresholds themselves
  (`MinSampleSize` 20, `StaleAfter` 90 days, `MinPostApplyRuns` 10, `RegressionCostRatio` 1.25,
  `RegressionReworkDelta` 0.2) are every one of them a reasoned guess, and cannot be calibrated
  without exactly that corpus.

There is also the owner's own standing rule that a repo goes public only for a concrete demonstrated
benefit, never to unblock a feature.

This section used to claim the benefit was that **the plugin marketplace requires a public
repository**, and called that the forcing argument. Checked against the documentation while building
B6c, and it is false: a marketplace can be private, and installs from it work for anyone with git
credentials to the repo. The owner installing Loom's own plugin on their own machines needs nothing
public at all. Public is only required for *strangers* to install without access being granted.

So the forcing argument is weaker than it was written to be, and the honest version is narrower:
going public buys distribution to people the owner does not know. That may well be worth it, but it
is a choice about who this tool is for, not a prerequisite dropping out of a tooling constraint. If
the answer is "for me, on my machines", a private marketplace already does the job and B6d has no
deadline.

#### Order

~~B6b first~~ **done**. ~~Then B6a, then B6c~~ - B6c went before B6a, because the discovery above
reverses their coupling: a private marketplace works today, so the plugin is usable now, whereas
signed releases only matter once there are strangers downloading binaries. B6a is therefore part of
going public rather than a prerequisite for the plugin.

Remaining: **B6a** if and when B6d is decided yes, and **B6d** itself, still gated on at least one
evidence path firing on real data. Both of the pieces worth having regardless of that decision -
B6b and B6c - are done.

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
