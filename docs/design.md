# Loom - an asset lifecycle and routing engine for Claude Code

Status: design agreed 2026-09-11.
Licence: Apache-2.0. Repo: personal GitHub, private until v0.1.0, then public.
Target: full open source project, distributed via the Claude Code plugin marketplace.

**B1 done (2026-09-11):** ingest, ledger, `loom report` - working, tested, run against real
history. Two real findings along the way, both in `docs/transcript-schema.md`: the subagent
completion record is an XML-like `<usage>` block inside a `queue-operation` line's text content,
not JSON fields, and does not reconcile with a naive sum of the agent's own transcript; and
`message.model` can be the literal string `"<synthetic>"` (a locally-injected status/error line,
always zero usage) - both handled, tested, documented.

**B2 done (2026-09-12):** asset discovery (skills, agents, plans, hooks, per-project memory -
`internal/asset`), selector (`internal/selector`), MCP server over stdio exposing
`query_ledger`/`get_recommendation`/`list_proposals`/`record_outcome` (`internal/mcp`), and
`loom advise`/`loom serve` CLI commands. Two real findings running `loom advise` against this
machine's actual skills: several real skills have no YAML frontmatter at all (just a `#` heading) -
fixed with a first-heading fallback for the description signal; and naive word-overlap scoring
without stopword filtering produced false-positive matches on shared function words ("the", "a",
"and", "for") - fixed by filtering a stopword list before scoring. `list_proposals` correctly
returns empty - nothing writes to that table until B5.

**B3 done (2026-09-12):** policy table, model pinning, generated agent definitions. Shipped in
four slices:

- **B3a** - `runs` now records `agent_type` and `effort`. Neither existed, so per-agent-type policy
  had nothing to key on. `agentType` lives only in the `.meta.json` companion beside each subagent
  transcript; `effort` is top level on assistant lines, not inside `message`. This closed a standing
  open item in `docs/transcript-schema.md`.
- **B3b** - `loom report` gained per-run cost, cost concentration, a by-agent-type breakdown, and
  the most expensive runs (closes #10). Totals alone invert the real ordering when run counts
  differ, which the policy engine would otherwise have inherited.
- **B3c** - policy resolution per agent type: stored, then evidence meeting `MinSampleSize` (20),
  then a shipped default. Every `Decision` carries `Source` and `SampleSize`, so constraint 11 is
  enforced by type rather than convention. `loom policy` shows it; `set`/`unset` give the
  deliberate override and its one-command revert.
- **B3d** - `loom policy render` writes agent definitions into Loom's own directory, never a
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
  Loom has no hook into writes, constraint 8 forbids it writing to human-authored files, and
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

Loom never reads or writes any external coordination state. Asset discovery scans skills, agents,
plans and per-project memory only, so a file-based exchange convention elsewhere in `~/.claude/` is
neither ingested nor interfered with, and its content never reaches the ledger (constraint 6).

**Pick this up on a personal machine.** Everything below is generic by construction, with no
employer context in it. Build it on personal hardware, on personal time, under personal accounts,
and keep it that way: the codebase must contain nothing derived from any employer's data, which is
both an IP-hygiene position and a hard requirement for publishing at all.

---

## Problem

Claude Code accumulates durable assets: scratch drafts, memory, skills, plans, hooks, agents,
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
combination of features to reach for on a given task. Proposes asset promotions and retirements as
reviewable diffs.

It measures and recommends. The human applies. That split is deliberate and permanent for anything
with blast radius.

---

## Design constraints, non-negotiable

The tool ships to strangers whose setups look nothing like the author's. Every one of these exists
to stop the design collapsing into one person's habits.

1. **Useful at n=0.** A new user with no history benefits immediately. Ship defaults reasoned from
   first principles; personalise as evidence accrues. Never require a corpus to be useful.
2. **Discover, never assume.** Find assets by scanning the standard Claude Code locations and the
   current project. No required layout, no required naming convention, no assumption that any given
   asset type is even in use.
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
9. **Asset-derived text is data, never instructions.** A skill/agent/plan's name, description, or
   heading is read off disk and re-served verbatim through `get_recommendation` into whatever session
   called it - that is untrusted content re-entering another agent's context, the same shape of
   problem as Candor's "all ingested telemetry is untrusted" rule. Loom cannot force a downstream
   client to treat it as data rather than commands, but it bounds the blast radius: every such field
   is length-capped, and every tool that returns one says so in its own schema. A cheap heuristic
   flag on obviously injection-shaped text (`suspicious`) is surfaced alongside a match - advisory
   only, per constraint 1's whole ethos, never a filter. Full detection is not attempted: phrase-based
   classifiers are gameable and a false sense of security is worse than an honest gap.
10. **Every accelerator ships with its brake, and nothing grows without a bound.** Any mechanism
   that can act, spend, or generate has its limit defined and enforced in the same change that
   introduces it, never in a later phase. A brake added afterwards is not a brake: the window it
   was missing is exactly the window the thing ran unattended.

   This bites hardest for a tool whose entire purpose is reducing accumulated material. A mechanism
   that answers "too many assets to keep track of" by producing more assets has made the
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
   discovered assets carry their names and descriptions. Keeping the ledger on the machine that
   produced it is what makes Loom safe to point at a sensitive corpus at all.

   Corollary for anything published - docs, examples, issues, screenshots, write-ups: it comes only
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

Eight asset types on two axes. The axes are what make the promotion rules principled rather than
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
enforcement**, and Loom cannot do it. It has no hook into what a session writes, constraint 8
forbids it writing to human-authored files, and enforcing would require a blocking `PreToolUse`
hook - the blast-radius category, and a direct contradiction of "it measures and recommends, the
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

**Files are the more enduring choice here.** Every durable Claude Code asset is a file: skills,
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

- `assets` - one row per known asset instance, with type, path, status
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

**Serve.** Local only, stdio. `loom serve` runs the MCP server over stdio and blocks until the
client disconnects - no daemon, no socket, no TCP, no network egress. A client spawns it as a
subprocess per its own MCP server config, one process per session.

### 2. Integration - MCP first

**An MCP server over stdio is the primary surface.** It works in any MCP client, needs no edits to
anyone's settings file, and installs in one line. As specified here it exposed four tools; narrowed
to `get_recommendation` alone - see "MCP server shape, narrowed further", B7's open items.

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
permanent design decision, not a placeholder to revisit. It pulls from the ledger - most expensive
runs this session, denied/corrected tool calls, assets touched - and writes that as the resume
pointer; `session start` reads it back as the briefing. It never decides what Claude Code's own
compaction keeps or drops. Same reasoning as "It measures and recommends. The human applies." above:
a hook that prunes or rewrites context has the same blast-radius shape as a hook that denies or
rewrites tool calls (Axis 2, cost shape) - cheap-if-wrong stays the only kind of hook Loom ships.
Not yet built - B2 shipped selector + MCP server only, no hooks.

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
them. A diff-and-decide pattern applied to assets.

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
- Asset use counts and staleness.

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

The split falls straight out of constraint 8 and decides everything else:

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

Apply is deliberately **CLI-only**, for the reason below: applying changes policy, and an assistant
can call an MCP tool without the human asking, so the terminal is where a decision that changes
state belongs. At the time this was written, `list_proposals` and `dismiss_proposal` were also
exposed over MCP, since listing is read-only and dismissing only hides a suggestion. Both later moved
to CLI-only too, for a different reason - see "MCP server shape, narrowed further" under B7's open
items.

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
- **B7** Context occupancy: what fills the window, how fast, and what compaction costs. Plus the
  three credibility issues the advisory half rests on. Scoped in detail below.

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

#### The README's install command does not work, filed and fixed - BUILT 2026-09-23

Found on the first real trial on a second machine (AMC), filed as loom #57: `go install
github.com/teerakarna/loom/cmd/loom@latest`, the README's headline command, fails for everyone while
the repo is private, including the owner with valid credentials - `sum.golang.org` 404s on a module
it cannot fetch, and the proxy's fallback to a direct `git ls-remote` does not go through the normal
credential helper, so it prompts for a username with prompts disabled. The issue itself framed the
real question correctly: this is a symptom of "go public or stay private", not a bug to patch around
either way, so it stayed unimplemented pending a decision rather than getting a quick unprincipled
fix.

Checked directly against this section's own gate before deciding: `pin_model`/`retire_asset`/
`revert_policy` proposal counts and the `policies` table, on this machine's real ledger, all still
zero. The gate is unmet, so **stay private**, consistent with what this section already committed to
rather than a fresh call. README and `plugin/README.md` now lead with a private-install path
(`GOPRIVATE=github.com/teerakarna go install ...`, or build from a clone) instead of the command that
does not work, and say plainly that this is deliberate, not a stale README - it goes back to a
one-liner once B6d actually fires.

#### A pending proposal never withdraws itself - BUILT 2026-09-23

Issue #40, found while confirming #38: `UpsertProposal`'s dedupe rule is careful about two cases
(evidence unchanged, leave alone; evidence changed, re-raise) and silent about a third - evidence
gone entirely. A proposal whose facts stopped holding just sat there, pending, quoting numbers that
were no longer true, until a human dismissed something that was never wrong so much as stale. That
is the alert-fatigue failure mode this project criticises other tools for, arriving through the back
door of its own advisor.

New `ProposalWithdrawn` status, distinct from dismissed for the reason the issue gave: "the situation
changed" and "a human rejected it" are different events, and collapsing them into one status loses
the thing the ledger is for. `ledger.WithdrawStalePending` marks pending rows withdrawn when the
current generated set does not contain them; `propose.Store` calls it before its own upsert loop, not
after - `MaxPendingProposals` counts `status = pending`, so withdrawing stale rows first is what lets
a genuinely new proposal use the slot a withdrawn one just gave up, in the same `loom propose` run
rather than the next one. `Apply` now also refuses a withdrawn proposal outright, for the same reason
it already refused an applied one: acting on it would mean acting on evidence that no longer holds.

Dismissed and applied rows are untouched by the new mechanism - confirmed by a test that dismisses
one proposal, withdraws another, and checks each kept its own status rather than one leaking into the
other.

**Three real bugs found by `/code-review high` before merge, empirically verified, not just
theorised.** The most severe one undid the whole point of this section: `UpsertProposal`'s original
"evidence unchanged, leave alone" fast path did not distinguish withdrawn from dismissed, so a
proposal whose evidence is static content rather than a daily-drifting number - the four B7c
memory-finding kinds, `broken_link`/`unreachable_asset`/`filename_slug_drift`/
`promote_memory_duplicate` - could never return to pending once withdrawn even once, contradicting
this section's own stated point that a withdrawal, unlike a dismissal, is not meant to be sticky.
Fixed by treating a withdrawn row as equivalent to "no row" for the purposes of that fast path: it
revives on unchanged evidence rather than staying stuck, and - the second bug this surfaced - the
revival now also clears the same `MaxPendingProposals` cap a brand new proposal would, since without
that check a burst of revivals could silently exceed it. Third: the dedupe key `WithdrawStalePending`
took was a `"kind|subject"` string concatenation, and `subject` is a raw filesystem path that can
legally contain `|` on POSIX - replaced with `ledger.ProposalIdentity`, a struct key, collision-proof
by construction rather than by the absence of an unlucky filename. `WithdrawStalePending` also moved
its per-row updates into one transaction, matching `ReplaceAssetUsage`'s existing pattern, rather than
N unbatched round-trips with no rollback on a partial failure.

A fourth, lower-severity finding was filed rather than fixed here: loom #59, a transient unreadable
memory store (B7c's own deliberate degrade-not-block tolerance) can now cause a one-pass "flicker"
where withdrawal wrongly retracts real proposals for that store, before this section's own revival fix
brings them back once the store is readable again. Narrower and self-healing after the fix above, and
a real fix needs `internal/asset` and `internal/ledger`'s withdrawal step to share a concept ("which
stores were actually scanned this pass") neither currently has - a design decision, not a bolt-on.

### B7 scope, agreed 2026-09-22

Two independent reassessments arrived at the same place within a fortnight. One was written on a
machine with no checkout, reading the repo over the GitHub API against a context-lifecycle spec. The
other was measured here, against real transcripts. They agree on the diagnosis and on which verbs to
refuse, and the measurements settle two questions the blind reassessment had to leave open. Both are
folded in below.

#### The active-steward role, refused for the fifth time

An aligned spec proposed an `execute_context_action` tool: retire a rule, filter a hook, compact a
plan, consolidate assets. Each of those writes to a human-authored file.

**DECIDED 2026-09-22 by the owner: not at all, at this stage.** Not as a loom tool, and not as a
separate tool alongside it either. Constraint 8 stands unchanged, and B7c is settled before it
begins: promotion emits a rendered diff, never a write.

This is the fifth time the same role has been examined and declined, which is worth stating plainly
so it is not re-derived a sixth time:

1. Constraint 8 itself, written because a tool in this space once shipped a version that deleted
   users' hand-written hook entries
2. B4, where lane write enforcement was cut as a contradiction of "measures and recommends, the
   human applies"
3. B5, on proposals touching user files: render a diff, nothing more, never
4. B2's pre-compact note, answering whether Loom should take an active role in context lifecycle
   before the question was asked again
5. This decision

The reasoning is kept rather than the conclusion alone: if it is ever wanted, it is a separate tool
consuming Loom's ledger over MCP, never a write path bolted in. Bolting it on requires deleting
constraint 8, and constraint 8 is why this design has held up under scrutiny.

The same decision disposes of `context-mode`'s mechanism, which Loom inherited motivation from and
not implementation. Storing raw assets out of band and handing the model a reference is a durable
secondary copy of whatever was in the session. It would delete the planted-secret test that backs
constraint 6, which is the whole safety argument on any machine that has seen confidential work. It
also overlaps the host's own compaction rather than complementing it. Measured here: native
compaction already achieves a **97.0% mean reduction** across eleven real events, so the mechanism
buys a marginal improvement over something that already exists, at the cost of Loom's strongest
property.

#### The framing moved onto a different metric

Loom measures **spend per run**. The new framing is about **context occupancy**: how fast the window
fills, what fills it, and what that costs. Both come off the same transcripts. They are not the same
number and Loom computes only the first. Nothing in the ledger answers "how full is the window, and
what put it there".

That is the real gap, and it is bigger than any tool name in the spec. It is also the cheapest to
close honestly, because occupancy is derivable from data already ingested, needs no content store, no
write access, and no interrupt.

#### What the measurements settled

Three findings from the personal corpus that the API-only reassessment could not reach.

**Tool output is the thing that fills the window, and one tool dominates it.** Across the lane:

| bucket | result output | leading callers |
|---|---|---|
| file I/O | 32.2 MB | Read 539, Edit 712, Write 317 |
| shell | 2.96 MB | Bash 3,340 |
| MCP server | 1.33 MB | Drive `search_files` 43, `read_file_content` 51 |
| web | 329 KB | WebSearch 88, WebFetch 60 |
| harness / UI | 109 KB | ExitPlanMode, AskUserQuestion, ToolSearch |
| delegation | 26 KB | Agent 21, TaskCreate 38 |

**Read alone is 86% of all tool output**, 539 calls averaging 60KB. This is the actionable half of
occupancy and it was absent from both the spec and the reassessment, which reasoned about how fast
the window fills without asking what fills it.

**Compaction does not need modelling.** An early attempt here inferred compaction from a drop in
`cache_read` and produced 42 candidates, every one of them a `<synthetic>` placeholder record with
zero tokens in every field. A 100% false positive rate. The host already writes the ground truth as a
`compact_boundary` system record carrying `compactMetadata`: `trigger`, `preTokens`, `postTokens`,
`cumulativeDroppedTokens`, `durationMs`, and `preCompactDiscoveredTools`. Read it; do not infer it.

Measured across the lane: 11 compactions, ~6.5M tokens dropped after deduping, **31.4 minutes of
wall clock spent compacting**. That last figure is not a token cost and nothing reports it anywhere.

**Occupancy does not depend on the asset-to-run join (#39).** The reassessment left this open and
suspected it. Confirmed: `tool_use` blocks carry the tool name directly, so tool-output accounting
and compaction pressure need only a tool name and a byte count. The join is per-asset; occupancy
is per-session and time-ordered. They are independent, which means **B7b can go before B7a** for
faster signal, though B7a still gates every claim that uses the word "unused".

#### B7a. Close the three credibility issues (#42, #39, #38) - BUILT 2026-09-22

Nothing else is worth building on an advisor whose central claim it cannot support. Three of five
open issues undercut it: #39 means "unused" cannot be answered, #41 means every promotion rule is
still prose, #38 means retirement can never fire for an asset that exists on disk.

- **#42**, skill discovery: require `<name>/SKILL.md`. Report a flat `.md` in a skills directory as a
  distinct finding, "present but never loadable", rather than counting it as a skill. Worth doing for
  its own sake and not only Loom's: six personal skills on another machine turned out to be dead this
  way, and the same measurement here found eight of nine.
- **#39**, asset-to-run join: record which discovered assets a run touched. Prerequisite for
  every staleness or disuse claim.
- **#38**, retirement condition.

**Measured before designing, not assumed.** String-matching an asset's name or path against
transcript content was tried by hand first and rejected on the same evidence the handover's worked
example warned about: every session's system prompt lists every discovered skill's name and
description whether it fires or not, and a `tool_result` can echo an asset's name back as plain
text from something unrelated (confirmed here: a `Read` of one file quoted a skill's name in
passing, produced by this very machine). Both would have reproduced the near-identical count for
every asset the handover already flagged as a dead end.

**#42 shipped as scoped.** `scanSkillDir` replaces `scanMarkdownDir` for skills only - agents, plans
and memory keep the flat-file convention, which is correct for them. A flat `.md` in a skills
directory is now `KindReference`, not `KindSkill`. Run against this machine's real skills directory:
9 real skills, 1 reference - a leftover `records-management.md` sitting next to its own
`records-management/SKILL.md`, exactly the ambiguity this issue was filed against, on this machine,
not a hypothetical.

**#39 shipped narrower than the credibility-issue framing implied, on the same measurement
discipline B7b used.** Two structured signals only, both read from a `tool_use` block's own `input`
field, never from message text: a `Skill` invocation's `skill` name, and a `Read`/`Edit`/`Write`
call's `file_path`. Agent usage needed no new signal at all - `runs.agent_type` already existed for
B3a's policy attribution, so it answers "was this agent invoked" by itself. Hook usage has no
reliable transcript signal and is out of scope, the same conclusion B7b reached for hook output
volume. A new `asset_usage(run_id, asset_path, uses)` table holds the result, resolved against
the assets table at write time - a skill name or file path matching nothing currently discovered
is dropped, not guessed at.

**#38 turned out smaller than its own issue text once #39 existed.** No new column: retirement's
staleness clock is `last_used` where `asset_usage` has an entry for the path, falling back to
`first_seen` - stable, never reset by a later discovery pass - when it does not. `last_seen` is
untouched and keeps answering its own question (`MarkStaleAssets`, is it on disk). The regression
test reproduces the original bug report exactly: discovery runs three times over weeks on an
asset nothing ever uses, staying on disk (`status = 'active'`) throughout, and the proposal now
fires - which it could never do before, for anything still present.

#### B7b. Occupancy metrics, and `loom context` - BUILT 2026-09-22

Measurement only. Two new tables, both derived metrics and identifiers, so constraint 6 holds:

```
tool_usage(run_id, tool_name, calls, result_bytes)
compactions(run_id, seq, trigger, pre_tokens, post_tokens,
            dropped_cumulative, duration_ms, at)
```

Per session: tool output by tool and by bucket; cumulative input tokens over time and the derived
slope; compaction events, time-to-first-compaction, and wall clock spent compacting; cache-read
share, already computed; per-fire hook output volume, since hook stdout lands in the transcript.
Surfaced through a read-only `loom context` and a `query_ledger` dimension.

Stated limits, up front rather than discovered later:

- **Bytes are not tokens.** Store the measured byte count; label any token figure an estimate. JSON
  and code tokenise worse than prose and a quiet conversion would make every downstream number wrong.
- **No attribution from compaction to cause.** You will see that Read dominates output and that
  compaction fired. "This Read caused it" is not derivable.
- **Resumed sessions carry the prior session's compaction records**, so the same event appears in two
  transcripts. Naive summing inflated the first measurement here by 1.7M tokens. Dedupe on boundary
  identity, and test it: this is the exact shape of the 2.12x over-count bug.

B7b also accrues far faster than per-agent-type run counts, so it is the cheapest route to satisfying
B6d's own evidence gate.

**Built narrower than specified, deliberately.** Tool output by tool and by bucket, and compaction
count/dropped-tokens/wall-clock, are what shipped - both surfaced through `loom context` (read-only,
same reason `loom status` is: this answers "what does the ledger already know", never "go find out")
and a `query_ledger.occupancy` dimension, the latter capped at the top 15 tools per constraint 10
(`loom context` shows the full table). **Cumulative input tokens over time and its slope,
time-to-first-compaction, and per-fire hook output volume did not ship in this pass** - none of them
change what the owner asked for (docs/design.md, "What the owner wants loom to produce"), and adding
them unmeasured would be exactly the scope creep B4's write-enforcement dial was cut for. Revisit
if a finding actually needs them.

Two things the plan above did not spell out, settled during the build:

- **`compactMetadata.cumulativeDroppedTokens` is not what gets summed.** It is cumulative within one
  session lineage, so summing it across several compactions in the same corpus double-counts. Ingest
  sums `preTokens - postTokens` per event instead - not cumulative, safely summable, and exactly what
  "tokens dropped by this compaction" means. See docs/transcript-schema.md, "compact_boundary".
- **The privacy verification this section's own checklist calls for did not exist.** "Ingest a
  fixture containing a planted secret, then grep the database for it" has been in the design doc's
  Verification section since B1, but no test anywhere did it. Built now
  (`internal/ledger/privacy_test.go`, `TestNoContentStored`) - generic over the schema (reads table
  names from `sqlite_master`), so it covers B7b's two new tables and needs no update when a later
  slice adds another.

Run against this machine's own corpus post-build: 11 compactions, 6.82M tokens dropped, 30.6 minutes
wall clock, Read 561 calls / 30.7 MiB (86% of tool output measured on a smaller slice of the same
corpus earlier - see "What the measurements settled" above). Consistent with the hand-measured
figures this section was scoped against, which is the actual test of whether the tables mean what
they claim to.

**A real correctness bug survived that "consistent with the hand-measured figures" check, and
`/code-review high` is what found it, not the tests or the dogfooding above.** `tool_usage` and
`asset_usage` were both written as one row per `(run_id, tool_name)` / `(run_id, asset_path)`,
aggregated at ingest time - with no per-event identity, unlike `compactions`, which had
`boundary_uuid` from the start specifically to dedupe a resumed session's replayed history. Ordinary
`tool_use`/`tool_result` lines turn out to replay the same way compact_boundary records do -
confirmed directly: 325 of 1203 `tool_use` ids shared between one real session and its resumed
continuation - so both tables double-counted every call a resumed session's transcript replayed.
The Read/compaction figures quoted just above are themselves overstated by however much of this
machine's corpus went through a resume, which was not separately measured.

**Fixed by giving both tables the same identity `compactions` already had.** Each `tool_use` block
carries its own globally unique `id`; `tool_usage`/`asset_usage` now store one row per id
(`tool_use_id` as the primary key, `ON CONFLICT DO NOTHING` on insert), aggregated at query time
instead of at write time. A ledger built before this fix has the old, un-deduped shape; `migrate`
drops and rebuilds both tables the next time `loom` opens it; rebuilt at the next `loom report`,
which is the only reasonable trigger to backfill it, and see issue #49 for the gap that already sits
under this exact situation.

The same review pass, verified against the code before acting on any of it, found three more real
issues in the same two features: `retireStaleAssets` never actually checked `runs.agent_type` for
agent-kind assets, contradicting this section's own claim two paragraphs up that agent usage
needs no new signal - a custom agent invoked constantly via the `Agent` tool could be proposed for
retirement as "never used" purely because that tool isn't Skill/Read/Edit/Write. `loom context`
printed nothing about compaction at all when a lane had compactions but no tool-output rows, because
its early return covered both sections at once. `BuildAssetLookup` had no `ORDER BY`, so two
assets sharing a name (a project-level skill overriding a global one - `discover.go` scans both
dirs by design) resolved to whichever row SQLite felt like returning that call. All three fixed
alongside the dedup fix, each with its own regression test reproducing the original failure shape.

#### B7c. Promotion rules as read-only proposals (#41) - BUILT 2026-09-22

Turn the promotion-rules table into code emitting `promote_*` proposals, each carrying evidence and a
rendered diff into Loom's own proposal directory. Same output as the spec's `promote_to_mechanism`,
minus the write.

One rule is already mechanically detectable with no inference at all, and it should be the first:
**a memory file that exists byte-identically across three or more project stores is a cross-project
fact in the wrong mechanism**, and belongs as a reference skill. Measured here: 6 stores, 58 memory
files, 17 exact duplicates, 5 files appearing in three stores each. Hashing only. No threshold, no
recurrence inference, no content retained.

The same pass covers three further structural checks that need no judgement: broken `[[links]]` (3 of
6 here), assets absent from the index that loads them and therefore unreachable (2 here), and
filename-to-slug convention drift, which is what breaks the links (4 here).

Add path-scoped rules to discovery in the same change, since the spec names them and Loom does not
model them. Discovery only: an orphaned rule or an overlapping pair becomes a finding, never a
deletion.

Constraint 10 applies and is satisfied in the same PR, not deferred: state the generation cap, the
dedupe key and the retention rule before merging.

**Built as scoped, with one piece deferred rather than guessed at.** The four structural checks
shipped: `internal/asset.DiscoverAllMemory` walks every project's memory store
(`~/.claude/projects/*/memory`) cross-project - the one place in Loom that needs a wider view than
"home plus the current project", since duplicate detection and index-reachability only mean anything
across stores. Constraint 6 holds throughout: files are hashed and scanned for `[[links]]`, never
retained. Four new proposal kinds route through the existing B5 machinery unchanged
(`propose.Store`/`UpsertProposal`), which is what satisfies constraint 10 without a separate
mechanism: the same `MaxPendingProposals` cap (20) and the same `(kind, subject)`-keyed,
evidence-hash re-raise rule already governed B5's three kinds and now governs these four too.

**Path-scoped rules: discovery not built, and said so rather than guessed.** "The spec names them
and Loom does not model them" was the extent of the direction, with no measured numbers behind it
anywhere in this document, unlike every other B7c check. The natural reading - CLAUDE.md files as a
new asset kind - runs into a real gap: Loom's other discovery is scoped to "home plus the current
project" precisely because those are Claude Code's own standard locations (constraint 2), but a
CLAUDE.md hierarchy lives under a user's own workspace tree, which has no standard root Claude Code
defines. Guessing at one (this machine's own `~/projects/{work,personal,public}` convention, say)
would be encoding one person's layout into the tool, which the design constraints open by forbidding.
Deferred rather than built on a guess - revisit if a concrete shape turns up, the same treatment
semantic staleness already got below.

**Run against this machine's own corpus, all four checks firing for real:** 5 duplicate groups
spanning 3 stores each, 5 broken links, 3 unreachable assets, 27 filename/slug drift cases (17
substantive - e.g. `feedback_working_preferences` renamed to `working-preferences` at some point
without the file following - 10 a systematic underscore-vs-hyphen convention difference, both real
drift by the same definition, not distinguished further since the design doc's own rule draws no
line between them). `loom propose` on the clean, reset local ledger returned exactly 20 - the
generation cap doing its job.

**A real bug found and fixed before this shipped, not after.** The MCP `list_proposals` handler
originally called `os.UserHomeDir()` inside the request handler itself. That made every test of it
non-hermetic: `go test` was scanning whichever machine happened to run the suite's own real,
private `~/.claude/projects/*/memory` files, and `TestListProposalsEmpty` failed outright once B7c's
generator started finding real findings on the developer machine that ran it. Fixed by resolving
`home` once at server construction (`NewServer(db, home)`, threaded from `cmd/loom/serve.go`'s own
`os.UserHomeDir()` call) rather than per-request, and test setup now passes an isolated `t.TempDir()`.
Caught by running the test suite, the same discipline that found B7a/B7b's resumed-session
double-count - the tests are what caught this one, which is the system working as intended.

**Then `/code-review high` was run against the PR before merging, not after, for the first time this
project has done that.** Three more real, verified bugs, all fixed in the same PR:

- **A permission problem on one project's memory store took the whole scan down.**
  `DiscoverAllMemory` only tolerated `os.IsNotExist` on a per-store read, propagating anything else -
  and because this function scans dozens of stores at once, unlike the single-project `Discover()`,
  the blast radius of one bad store was every other store's findings too, and beyond that everything
  `list_proposals`/`loom propose` return, including proposals with nothing to do with memory. A
  regression against constraint 7 in spirit even though the specific pattern (only special-casing
  `IsNotExist`) already existed in `scanMarkdownDir` - it mattered here because of the fan-out, not
  because the pattern itself was new. Fixed: any per-store read error now skips that store, not the
  scan; `MemoryIndex`'s signature dropped its `error` return entirely, since every failure mode it
  can hit now collapses to the same empty-index answer.
- **`detectMemoryDuplicates`'s output order was randomized per call**, from ranging directly over a
  Go map keyed by content hash. Harmless until the total findings across every check exceeded the
  20-proposal cap - the exact situation this machine's corpus produces - at which point which subset
  of duplicate findings actually got a slot depended on map iteration order, so two back-to-back runs
  against identical, unchanged disk state could persist a different set each time. The "not a
  coincidence" claim above was true of the count, not yet of which 20. Fixed with an explicit sort by
  subject before returning.
- **The subdirectory memory convention (`topic/SKILL.md`) was invisible to every B7c check.**
  `DiscoverAllMemory` unconditionally skipped directory entries, while `scanMarkdownDir` - used for
  this exact kind in the single-project `Discover()` path - already treats a subdirectory containing
  its own `SKILL.md` as an equally valid memory asset. A `[[link]]` to such an asset would have
  been reported `broken_link` even though the target genuinely existed. Fixed by mirroring
  `scanMarkdownDir`'s two-shape handling.

Two findings from the same pass considered and not fixed, reasoning kept rather than the conclusion
alone: no caching of the per-call filesystem walk (matches `Discover()`'s own existing, uncached
behaviour exactly - not a regression, and premature caching risks a staleness bug bigger than the
walk cost at this corpus's measured scale); and a `ReadFile`-then-separate-`Open` pair in
`readMemoryFile` that could theoretically observe two different versions of a file edited mid-scan -
a real but vanishingly narrow race, self-correcting on the next run, not worth the complexity of a
single-read refactor for what it would prevent.

**Not in B7c: semantic staleness.** Tested here and rejected on measurement. Extracting the claims an
asset makes and checking whether they still resolve produced 44 candidates and 6 flags, all 6 false
positives: a slash command read as a path, two documentation examples, and two work-machine paths
correctly absent on a personal machine. Separating an assertion from an illustration needs to read for
intent, which needs a model, which is the cost this is meant to reduce. Recorded so it is not retried
without a new idea behind it.

#### B7d. Surfacing: MCP pull, and nothing else

No scheduler, no sweep hook, no session-start nudge. The trigger is the session asking, through
`list_proposals` and the new `query_ledger` dimension. The nudge was cut on measurement, not taste:
20ms budget, 960ms measured cold, and session start is exactly when the binary is cold.

If something must fire proactively, the only place it can go without reopening a cut decision is the
pre-compact hook, already specified as a pointer-writer with a 50ms budget and an
overwrite-never-append rule. It may carry an occupancy summary. It must not gate compaction.

#### B7e. Doc and code hygiene, first commit - BUILT 2026-09-22

Small, and all of it is drift between what the code does and what it says:

- `cmd/loom/main.go`'s usage string and `internal/mcp/doc.go` both say four MCP tools and omit
  `dismiss_proposal`. There are five. `doc.go` also still describes B2 as the current scope.
- The never-write rule is constraint **8**. Four places cite it as constraint 9, which is
  "asset-derived text is data". Residue from the renumbering that produced the append-never-insert
  rule.
- Line 444 still describes a Unix domain socket fast path. `serve.go` is stdio-only and says so.
- 19 em or en dashes remain in this file. The hyphen rule was made a rule elsewhere and this repo has
  not had the sweep. Mechanical, and worth doing in the same pass rather than drifting further.

#### MCP server shape, narrowed further - BUILT 2026-09-23

B7d kept `list_proposals` and the `query_ledger` occupancy dimension on the MCP surface alongside
`get_recommendation`, on the reasoning that pull beats a scheduler. Revisited once more before the
first real trial (installing on a second machine, AMC, to see how it actually changes a session's
behaviour): the MCP surface now carries **`get_recommendation` only**.

`record_outcome` is the one with an actual correctness reason to move, and it is the same reason
B5 already established for `loom propose apply`: it is a write, and "Apply is deliberately CLI-only
... an assistant can call an MCP tool without the human asking. The terminal is where a decision
that changes state belongs" (see B5 above) applies to it word for word - B7d exposing it over MCP
was inconsistent with B5's own rule and should not have happened.

The other two moves (`query_ledger`, `list_proposals`) rest on reasoning, not a measurement, and are
labelled as such rather than stated as settled fact:

- Every one of the four already has a CLI equivalent (`loom report`/`status`/`context`,
  `loom propose`, `loom propose dismiss`, the new `loom record-outcome`), and a live session can run
  any CLI command through its own shell tool. MCP earns its place only for the tool with no such
  substitute: `get_recommendation` needs to fire mid-task, on the hot path, with no human in the
  loop to type a command.
- The assumption, not yet measured: a tool's name and description sit in every session's system
  prompt the plugin is installed into, whether that session ever calls the tool or not, so three
  tools' worth of description text the CLI already covers is a standing cost with nothing measured
  to justify it. If a later trial shows pulling `query_ledger`/`list_proposals` via MCP is worth
  more than that cost, this reverses - unlike the `record_outcome` move above, which does not.

`internal/mcp/server.go`'s `NewServer` dropped to one registration; the now-dead wrapper types
(`LedgerReport`, `ProposalsOutput`, `Proposal`, `DismissInput`/`Output`, `OutcomeInput`/`Output`) and
their handlers were deleted rather than left unused, since the underlying logic they wrapped
(`db.Report`/`Occupancy`, `propose.Generate`/`Store`, `db.DismissProposal`) is exercised directly by
the CLI commands and by `internal/ledger`/`internal/propose`'s own tests - nothing lost coverage.
`NewServer` also dropped its `home` parameter, now unused since `list_proposals` (the one handler
that read it) is gone. Contract version bumped `v0.2.0` -> `v0.3.0` and `plugin.json` to match, per
`internal/mcp/plugin_test.go`'s own rule that the two must move together.

#### The "artifact" noun rename - BUILT 2026-09-23

Loom's artifact (a skill, plan, agent, hook or memory file) collided with the aligned spec's own
artifact (a versioned render block) - the other open item left for the owner rather than decided
during B7. Resolved to **rename, not keep**: shown the actual size of the change (roughly 440
occurrences across 35 Go files, the discovery package, a DB table needing a migration, MCP field
names, and every doc) plus a collision check against loom's own existing vocabulary, the owner chose
**`asset`** over the alternatives checked and ruled out - `resource` collides with MCP's own
first-class "Resources" concept (a real collision with something loom itself could plausibly expose
one day, arguably worse than the one being fixed), `definition` collides with `loom policy render`'s
already-named "agent definitions", and `fixture` collides with `testdata/`'s established meaning.

Mechanical, done in one pass: `internal/artifact` -> `internal/asset`, `Artifact`/`ArtifactRecord`/
`ArtifactRow`/`ArtifactUsageSummary`/`ArtifactLookup`/`ArtifactTouch` and friends -> `Asset...`,
`KindRetireArtifact`/`KindUnreachableArtifact` and their stored kind strings (`retire_artifact` ->
`retire_asset`, `unreachable_artifact` -> `unreachable_asset`) -> `Asset`, the `artifacts`/
`artifact_usage` DB tables and `artifact_path` column -> `assets`/`asset_usage`/`asset_path`, and
every doc. A new `migrateAssetRename` drops the old-named tables on a pre-rename ledger - both are
pure derived caches, rebuilt in full by the next `loom report`/`loom advise`, so nothing is lost;
verified against this machine's own real `~/.loom/loom.db`, backed up first.

**A real bug found by `/code-review high` before merge, not after.** The rename alone left a
pending proposal stored under an old kind string (`retire_artifact`, `unreachable_artifact`)
mis-reported: `propose.TouchesUserFiles` only recognized the new kind strings and defaulted anything
else to `false` - "safe to automate" - which is exactly backwards for a proposal that in fact
touches the user's files. Fixed two ways. `migrateAssetRename` now also deletes any `proposals` row
still carrying an old kind string, since `Generate` never emits one again and the row is permanently
dead. And `TouchesUserFiles` itself flipped its default from fail-open to **fail safe**: it now names
the two kinds that are genuinely safe (`KindPinModel`, `KindRevertPolicy`) and treats everything
else, recognized or not, as touching the user's files - so a kind this function has never heard of,
whenever that happens next, is never silently assumed safe again.

One exception kept as `artifact`, deliberately: `docs/design.md`'s own note that the `coordination`
table "stays in the schema as an empty, unused artifact of the original design" uses the word in its
ordinary English sense (a leftover), not Loom's concept - renaming it would have been wrong, not
thorough. `SECURITY.md`'s mention of "a fetched artifact" (a downloaded release binary) is the same
kind of exception. This document's own historical, dated build entries above (B1 through B7) were
renamed along with everything else, on the view that a pure terminology change is not the same kind
of edit as rewriting a historical *decision* - nothing about what was built or why changes, only its
label, so leaving half the document using the old name for "historical accuracy" would have made it
permanently self-contradictory against the code for no real benefit. This is different in kind from
how the MCP server shape section above treats its own history, where the actual tool inventory
genuinely differed at different points in time and preserving that is the accurate record.

#### Open, and deliberately not decided here

- **Provenance of the dogfooding figures already committed to this doc.** If any were measured on a
  corpus including employer sessions, that needs adjudicating before B6d rather than after. Aggregate
  derived metrics are a far weaker exposure than content and the figures are unattributable on their
  face, so this is probably fine. It should be a decision with a date on it, not an assumption.

#### Order

B7e first, since it is minutes and makes later failures attributable. Then **B7b**, because occupancy
is independent of the join, accrues evidence fastest, and feeds B6d's gate. Then B7a, then B7c, then
B7d last so surfacing earns its place on measurement the way B5's did.

## Verification

- **Ingest correctness.** Replay fixtures and assert computed subagent totals reconcile with the
  totals the transcripts themselves record. If they do not reconcile, the cost model is wrong and
  nothing downstream can be trusted.
- **Memory bound.** Ingest a large synthetic corpus under a hard RSS ceiling, proving streaming.
- **Latency.** Cold start under 20ms. Kill the `loom serve` process mid-session and confirm every
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

### Added for B7

- **Skill shape (#42).** Point discovery at a fixture tree holding both `foo/SKILL.md` and a flat
  `bar.md`; assert one skill and one "present but never loadable" finding.
- **The join is real, not inferred (#39).** Report over a corpus where a known skill was invoked;
  assert a non-zero use count against a known-dormant one showing zero. State the sample size, per
  constraint 11.
- **Compaction is read, not guessed.** Assert the recorded event comes from the host's
  `compact_boundary` record. A test that reconstructs compaction from a `cache_read` drop must fail:
  that inference was tried and was wrong 42 times out of 42.
- **Resumed sessions do not double-count.** Ingest two transcripts where the second resumes the
  first and carries the same compaction boundary; assert the event is counted once. Same shape as the
  2.12x over-count, so it gets its own fixture. Not compaction-only: `tool_usage` and
  `asset_usage` need the identical test, on the identical shape of evidence (a `tool_use` id
  shared between two runs) - missing here is exactly what let the double-count into both tables
  in the first place, caught only once by code review, not by this list.
- **Bytes stay bytes.** Assert no stored column holds a token estimate. The conversion belongs at the
  display edge, labelled, or downstream arithmetic inherits an error it cannot see.
- **The privacy property survives the migration.** The planted-secret test must pass unchanged after
  B7b's schema change. If B7b makes that test harder to write, the design drifted.
- **Write containment catches the refused verb.** The existing containment test is what stops
  `execute_context_action` arriving by increments. Extend its assertion set to the proposal
  directory and leave it strictly enforced.

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
  GitHub's native secret-scanning toggle - that feature requires the repo to already be public (or
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
