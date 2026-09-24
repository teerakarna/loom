# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- CI reduced from five billed jobs to two: `test`/`lint`/`govulncheck`/`plugin` merged into one `ci`
  job (Actions bills every job at least a full minute regardless of how little it runs), `secrets`
  kept split out for its own permission scope (`pull-requests: write`, full-history checkout).
  Prompted by a cross-session handover during a billing-driven Actions outage on another repo - loom
  itself was unaffected at the time, confirmed rather than assumed, but the practice applies
  regardless. Added a `concurrency` group cancelling a superseded PR run (never on `main`),
  `persist-credentials: false` on every checkout, and `scripts/ci.sh` to run the same gate locally.
  Branch protection's required checks updated to match (`ci`, `secrets`). Trades billed minutes for
  wall-clock time (parallel jobs became one sequential job), stated explicitly rather than left
  implicit. `/code-review high` found six real issues in the first version, all fixed: the
  govulncheck-action step's own internal checkout was silently re-persisting the token the job's
  checkout had just disabled (confirmed against the action's actual pinned source), and its
  `go-version-input` default was silently overriding the pinned Go version from `go.mod` the same
  way; a failure with no override stopped every later step in the merged job, unlike the independent
  jobs it replaced; the unpinned plugin-manifest install ran after the Go caches had accumulated
  instead of before; `scripts/ci.sh`'s exit code didn't distinguish a real failure from a missing
  tool; and its local tool versions had no visibility into CI's pinned ones. A second review round
  found three more, confirmed against the pinned action's actual source: `govulncheck-action`'s own
  internal `setup-go` step had no override and ran unconditionally regardless of the first round's
  fix, so Go was still being resolved and cached twice - fixed by dropping the action entirely and
  running `govulncheck` directly against the Go this job already set up once. `Build` was missing the
  `!cancelled()` condition every other step had, so an earlier failure could skip it while everything
  after it kept running anyway - fixed. `scripts/ci.sh` used `set -e`, so a local build failure
  silently stopped every later check from running at all - rewritten so every check runs and reports
  independently, matching CI's own guarantee, verified directly against an intentionally broken build.
  A third round found two more: `!cancelled()` alone doesn't check whether `checkout` itself actually
  succeeded, only that the job wasn't cancelled - a genuine checkout failure would still let every
  later step attempt to run against an empty workspace; fixed with an explicit
  `steps.checkout.outcome == 'success'` alongside it. And GitHub cancels a still-queued concurrency-
  group run the moment a new one joins the same group regardless of `cancel-in-progress`, so three
  quick pushes to `main` could silently drop the middle one's run entirely; fixed by keying the group
  on `github.run_id` for anything that isn't a `pull_request`, so every push to `main` gets its own
  group of one.

- MCP surface widened back to four tools (#62, the last of the AMC trial's four findings, and the one
  that reverses an earlier decision): `get_cost_summary`, `get_context_occupancy` and `list_proposals`
  restored alongside `get_recommendation`. The narrowing this reverses was explicitly labelled
  unmeasured reasoning, reversible if a later trial said otherwise - the first real trial did: a
  session connected to a real, 205-run ledger could see nothing about cost, occupancy or proposals.
  `dismiss_proposal`/`record_outcome` stay CLI-only; that half of the original reasoning rested on
  B5's settled write-at-the-terminal rule, not on the unmeasured cost claim, and this trial gave no
  reason to revisit it. `NewServer` regains its `home` parameter (constructor-injected, not resolved
  per-request - the same hermeticity fix as before). Contract version `v0.3.0` -> `v0.4.0`,
  `plugin.json` bumped to match. Verified for real over the wire: `loom serve`'s `tools/list` returns
  all four.

- README/`plugin/README.md` install instructions no longer lead with `go install
  github.com/teerakarna/loom/cmd/loom@latest` - it doesn't work while the repo is private, for
  anyone, including the owner (checksum-server 404, then a credential-less git prompt), found on the
  first real trial on a second machine (#57). Checked B6d's own "going public" gate against the real
  local ledger before deciding: no evidence-based proposal has ever fired, so staying private is
  correct, not a stopgap. README now leads with a private-install path instead.

- Renamed Loom's core concept from "artifact" to "asset" throughout the codebase and docs
  (`internal/artifact` -> `internal/asset`, `ArtifactRecord`/`ArtifactRow`/`ArtifactUsageSummary`/
  `ArtifactLookup`/`ArtifactTouch` -> `Asset...`, `KindRetireArtifact`/`KindUnreachableArtifact` and
  their stored kind strings -> `Kind...Asset`/`retire_asset`/`unreachable_asset`, the `artifacts`/
  `artifact_usage` DB tables and `artifact_path` column -> `assets`/`asset_usage`/`asset_path`),
  resolving the collision with the aligned spec's own "artifact" (a versioned render block) - an
  open item left for the owner since B7 was scoped. `asset` chosen over `resource` (collides with
  MCP's own first-class Resources concept), `definition` (collides with `loom policy render`'s
  "agent definitions") and `fixture` (collides with `testdata/`'s established meaning). A new
  `migrateAssetRename` drops the old-named tables on a pre-rename ledger; verified against this
  machine's own real `~/.loom/loom.db`. Renamed throughout this changelog's own history too, not
  just forward from here - every entry below describes code that has never shipped under any name
  but this one (nothing has been tagged or released yet), so there is no locked-in "as published"
  wording to preserve, unlike the MCP-narrowing entry above, which records an actual behaviour
  change over time rather than a pure rename.

  Reviewed before merge, and a real bug survived until it was: `TouchesUserFiles` defaulted an
  unrecognized proposal kind to `false` ("safe to automate"), which would have silently mis-reported
  a proposal stored under an old kind string as safe rather than as touching the user's files. Fixed
  by having `migrateAssetRename` also delete any `proposals` row still carrying an old kind string
  (dead the moment `Generate` stopped emitting it), and by flipping `TouchesUserFiles`'s default from
  fail-open to fail-safe: it now names the two kinds that are genuinely safe and treats everything
  else as touching the user's files.

- MCP server narrowed to one tool, `get_recommendation`. `record_outcome` moves for the same reason
  B5 already made `loom propose apply` CLI-only: it writes, and an assistant can call an MCP tool
  without the human asking, so the terminal is where a decision that changes state belongs - B7d
  exposing it over MCP was inconsistent with that rule. `query_ledger` and `list_proposals` move on
  reasoning rather than a measurement: both already have a CLI equivalent a session can reach via its
  own shell tool (`loom report`/`status`/`context`, `loom propose`), and a tool's description sits in
  every installed session's system prompt whether called or not - unmeasured, and reversible if a
  later trial says otherwise, unlike the `record_outcome` move. New `loom record-outcome` command
  replaces `record_outcome`. `NewServer` dropped its now-unused `home` parameter; contract version
  `v0.2.0` -> `v0.3.0`, `plugin.json` bumped to match.

### Added

- `loom advise --agent-type <type>` and `get_recommendation`'s new optional `agent_type` (#63, found
  on the AMC trial - `loom policy` showed four evidence-backed agent-type policies, `loom advise` on
  the same ledger still said "no history yet to personalize from"). When given and a policy exists,
  `selector.Recommend` uses that real, measured (or hand-set) policy directly instead of a cold-start
  keyword guess - `selector` stays database-free, the caller resolves and passes in the `*ledger.PolicyRow`.

### Changed

- `loom advise`'s cold-start heuristic no longer escalates to `opus` on a single incidental keyword
  (#63) - the query that found this, a debugging task, matched "why" and got `opus`/`high` although
  `opus` costs roughly 272x `sonnet`'s per-run cost on the corpus measured. `"why"` removed from
  `planningWords` (too common in ordinary debugging phrasing), and escalating now needs at least two
  distinct planning-keyword hits (`minPlanningHits`), not a one-word margin. Ambiguity now defaults
  cheap, not expensive.

- `loom advise`/`get_recommendation` no longer return nothing on a lexical near-miss (#64) - a real
  memory file named `devicefarm-public-devices-fail-device-gate` scored zero against a query
  containing "Device Farm" because "device"+"farm" (the query's spacing) and "devicefarm" (the
  asset's own naming) were different token strings. New compound tokenizer adds adjacent-word
  concatenations as bonus match tokens, on both sides, without inflating the score's denominator.
  `Recommend` also never returns fully empty when something, even weak, scored above zero - the best
  candidates surface with a new `BelowThreshold` flag instead of silence.

  Reviewed before merge, over two passes, four real bugs found and fixed. First pass: the
  compound-match fix broke `SkillMatch.Score`'s documented 0-1 range (a candidate whose own
  description independently repeats the query's phrase matched both the unigrams and their compound,
  reproduced directly at `1.5`) - fixed by clamping to `1`. Second pass, on the fix for the first: the
  query side was still scanned twice building two token sets from the same text - restructured into
  one shared word-splitting pass (`tokenizeWords`) that both the unigram and compound sets build
  from; the compound tokenizer was still joining `Name`+`Description` into one string before
  compounding, so the boundary between two unrelated fields could form a compound neither field
  actually contains (reproduced: an asset scored `0.6` against a query it should have scored `0.4`
  against, from a spurious "devicefarm" formed across a field boundary) - fixed with a new
  `compoundsAcrossFields` that merges each field's own compounds instead of concatenating first; and
  the score clamp's ties sorted in whatever order an unstable sort happened to land them - switched to
  `sort.SliceStable`. The same second pass also found `coldStartModel`'s tie case (equal
  planning/retrieval hits) correctly defaulted cheap but with a rationale claiming pure retrieval
  dominance it didn't have - split into its own case with honest wording.

- `loom propose --lane <lane>` (#68, found on the AMC trial - 14 of 20 pending proposals on that
  machine belonged to a different lane than the session running the command, printing another
  project's memory-store paths uninvited). Narrows the four B7c memory-finding kinds to one store;
  `pin_model`/`revert_policy`/`retire_asset` always show, since the policy they write is
  machine-global and filtering them by lane would misrepresent what applying one actually does.
  Display-only: `Store` still gets the full, unfiltered set on every run, so `--lane` can never
  withdraw another lane's still-valid proposals as a side effect of narrowing this one's view.

- Promotion rules as read-only proposals (B7c, #41): four structural checks over every project's
  memory store, cross-project via a new `asset.DiscoverAllMemory` (the one place Loom looks
  beyond home + the current project). A memory file byte-identical across three or more stores is
  a cross-project fact stuck in a per-project mechanism (`promote_memory_duplicate`); a `[[link]]`
  that does not resolve within its own store (`broken_link`); a file not linked from its store's
  own `MEMORY.md` (`unreachable_asset`); a filename that has drifted from its own frontmatter
  name (`filename_slug_drift`). All four route through B5's existing proposal machinery unchanged,
  so the generation cap (20) and evidence-hash dedupe already built for B5's three kinds cover
  these too, satisfying constraint 10 without a separate mechanism. Path-scoped rule discovery
  (the fifth item in scope) deferred rather than guessed at - no standard root for a CLAUDE.md
  hierarchy the way there is for Claude Code's own transcript/skill locations, and no measured
  numbers anywhere behind what "the spec" meant by it. Run against this machine's own corpus: 5
  duplicate groups, 5 broken links, 3 unreachable assets, 27 filename/slug drift cases;
  `loom propose` returned exactly the 20-item cap.

- `golangci-lint` now runs `gosec`, `sqlclosecheck`, `errorlint`, `unconvert`, `misspell` and
  `predeclared` alongside the existing standard set, free and zero new infrastructure. Real
  findings on the codebase as of this change: 20 `noctx` hits (deliberately not enabled - a
  single-user local CLI has no request lifecycle to cancel, so context-cancellable DB calls
  would be pure churn), 8 `G202`/5 `G304`/2 `G703` gosec hits that are structural false positives
  for a local CLI reading its own filesystem (excluded with a comment saying why, not silently),
  3 real `sqlclosecheck` hits (fixed), one real `errorlint` hit and one `predeclared` shadow of
  Go's builtin `max` (both fixed).

- The asset-to-run join (B7a, #39): a new `asset_usage(run_id, asset_path, uses)` table,
  populated from two structured signals only - a `Skill` tool_use's skill name, and a
  `Read`/`Edit`/`Write` tool_use's file_path - never from message text, which an earlier attempt
  confirmed would give every asset a near-identical count. Agent usage needed no new signal:
  `runs.agent_type` already existed for B3a. `loom propose`'s retirement check now uses this, via
  the #38 fix below, instead of `last_seen`.

- Context occupancy (B7b): two new tables, `tool_usage` (calls and result bytes per tool per run)
  and `compactions` (read directly off the host's own `compact_boundary` records, never inferred -
  an earlier attempt at inferring compaction from a `cache_read` drop was wrong 42 times out of 42).
  Surfaced through a new read-only `loom context` command and a `query_ledger.occupancy` dimension.
  A resumed session replays its prior compaction history verbatim, uuid included, so compaction rows
  dedupe on that uuid - naive summing had inflated an earlier measurement by 1.7M tokens. Bytes stay
  bytes throughout: no column holds a token estimate. Run against this machine's own corpus post-
  build: 11 compactions, 6.82M tokens dropped, 30.6 minutes wall clock, Read 561 calls / 30.7 MiB -
  consistent with the hand-measured figures docs/design.md's B7b section was scoped against.

- Claude Code plugin (B6c). `plugin/` carries a manifest and an MCP server registration, and
  `.claude-plugin/marketplace.json` makes the repository its own marketplace, so installing is
  `/plugin marketplace add teerakarna/loom` then `/plugin install loom@loom`. The plugin does not
  contain the binary and does not fetch one - it resolves a `loom` you installed, via a wrapper that
  checks `$LOOM_BIN`, PATH, and where the documented install methods actually put it, because
  `~/go/bin` is on an interactive shell's PATH and not on the one a desktop app inherits. Ships no
  hooks and no skill: both were specified, both were cut, and `plugin/README.md` says so rather than
  leaving the old description standing.

- `claude plugin validate --strict` as a CI job, plus checks in `internal/mcp/plugin_test.go` for
  the three things it does not catch - a command path that does not exist, a wrapper that has lost
  its executable bit, and a manifest version disagreeing with the MCP server being shipped. All
  three were planted and confirmed to pass validation while broken.

- Fixture-hygiene check (B6b), one of the gates the publishability rules require before this repo
  could go public. Catches the markers of real data in `testdata/` - absolute home paths, email
  addresses, a size ceiling, and the very long unbroken strings a real `thinking` signature
  produces - and requires every fixture to be declared in a manifest, so "this was written by hand"
  is an explicit claim in a diff rather than an assumption. Named hygiene, not provenance: nothing
  can prove from content that a file was not derived from a real transcript.

- Loop closure: applying a policy now records the baseline it was measured against, and later runs
  are compared against it. A regression surfaces as a `revert_policy` proposal carrying before/after
  evidence and saying what got worse; applying it restores the shipped default and re-opens the
  original question. Rework is weighed before cost, because a cheaper model that gets things wrong
  is not a saving.

### Fixed

- A root-scan failure (`<home>/.claude/projects` itself unenumerable) protected every lane-scoped
  pending proposal forever instead of only for a one-pass blip (#76, filed from #59's third review
  round) - a persistently wrong or misconfigured `$HOME` meant a stale proposal could occupy a slot
  against `MaxPendingProposals` long after its finding may have stopped being true. New singleton
  ledger table `memory_root_scan` tracks how long an unbroken failure streak has run (cleared the
  moment a pass succeeds, set only on the first failure of a new streak); new
  `RootScanFailureTolerance` (one week) - once exceeded, protection falls back to normal withdrawal.
  `Store`'s `coverage` parameter became `*asset.MemoryScanCoverage` to resolve a real ambiguity this
  surfaced: the zero-value struct was already the sentinel for "no scan attempted" (existing DB-only
  tests) but is also exactly what a genuine root-scan failure produces - `nil` now means the former,
  a non-nil pointer the latter, so a DB-only caller never starts a fake failure streak. Verified
  against the real binary for what a real clock can exercise, and directly against the timing logic
  for the week-long boundary a real clock cannot. `/code-review high` found two real gaps in the
  first version, both fixed: recording the streak was wrongly gated on something being pending, so a
  successful scan with nothing pending never cleared it, letting a later unrelated proposal inherit a
  stale, already-expired streak; and the streak's write was a separate `SELECT` then update, a real
  race between two `Store` calls close together, now one atomic `INSERT ... ON CONFLICT ...
  RETURNING`.

- `list_proposals`, `get_recommendation`, `get_cost_summary` and `get_context_occupancy` surfaced a
  raw `SQL logic error: no such table` string instead of an actionable message when the resident MCP
  server process had drifted from the ledger schema (#74, from a candor-rooted session on 2026-09-24).
  Root cause confirmed by `ps`/`lsof`, not guessed: every `loom serve` process on the machine held an
  old binary inode open, predating the artifact-to-asset rename, while `~/go/bin/loom` on disk had
  since moved on via ordinary rebuilds elsewhere - the plugin's wrapper script resolves the binary once
  at spawn and `exec`s it for the process's life, so a resident session keeps running stale code with
  no signal it has drifted. `query_ledger` never touches the affected table, which is why it kept
  working and masked the problem. Current source was already correct; new `explainIfStaleProcess`
  recognizes the error class at the point each handler returns it and tells the caller to restart the
  session, rather than leaving them to diagnose a driver string. Matches `"no such column"` as well
  as `"no such table"`, and states the diagnosis as a likely cause rather than an assertion, both
  fixed after `/code-review high` on the first version.

- A memory store whose `memory/` directory was transiently unreadable for one `loom propose` pass
  could have every one of its real, unchanged proposals wrongly marked withdrawn (#59, found by code
  review while shipping #40's withdrawal mechanism) - `DiscoverAllMemory` skipping the store for that
  pass looked, to `WithdrawStalePending`, identical to the store's findings having genuinely stopped
  being true. `DiscoverAllMemory` now also reports a `MemoryScanCoverage` (which store directories
  were found, and which of those were actually readable); `propose.Store`'s new `needsProtection`
  protects any pending lane-scoped proposal this pass could not confirm is truly gone, treating it as
  reproduced rather than stale until a pass that actually looks again says otherwise. New
  `propose.LaneScopedKinds`/`propose.EvidenceStores`, generalized from equivalent logic `loom propose
  --lane` (#68) already had in `cmd/loom`, now shared by both. A second `/code-review high` round
  found two more gaps in the first version, both fixed: a root-level scan failure (not just one
  store) fell through to unconditional withdrawal, reproducing #59 one directory level up; and a
  permanently deleted store's proposals were protected forever instead of ever withdrawing, the
  opposite failure. `WithdrawStalePending` also no longer re-queries pending proposals `Store` already
  fetched (also found by review). Verified against the real binary through all three scenarios: a
  store made unreadable (proposal stayed pending, then withdrew once genuinely fixed), a store deleted
  entirely (proposal withdrew), and `$HOME` pointed at a directory with no `.claude/projects` at all
  (proposal stayed pending rather than mass-withdrawing). A fourth round found one more live bug: a
  protected proposal's `Summary`/`Rationale` came back blank over MCP, since `list_proposals` built
  that text from a map keyed only by this pass's freshly generated proposals - a protected row is
  pending precisely because it wasn't reproduced, so the lookup always missed. Fixed by moving
  `cmd/loom`'s existing per-kind summary logic (unaffected by the bug itself) into shared
  `propose.SummaryFor`, with a new, honest Rationale fallback stating the row was not rescanned this
  pass.

- `broken_link` stayed silent on a `[[link]]` to a real skill, indistinguishable from a permitted
  forward reference to a not-yet-written memory (#66, split out from #65) - a `[[link]]` only ever
  resolves against a memory's own frontmatter name, never a skill's, so a link naming a real skill
  will never resolve as written, unlike a genuine forward reference. New
  `asset.DiscoverGlobalSkills(home)` scans `~/.claude/skills` (the one skill location that is
  reliably enumerable without a specific project's cwd); `detectBrokenLinks` now flags a link whose
  target is a known skill, with rationale explaining what it actually is rather than staying silent.
  Evidence carries a new `target_is_skill` field, set only when true (never a literal `false`) so a
  plain cross-store finding's evidence hash stays unchanged - an unconditional field would have
  changed it for every existing cross-store `broken_link` proposal, silently reverting any previously
  dismissed or applied one to pending on the next pass (the same regression class #77 fixed, caught
  by `/code-review high` before this shipped, confirmed fixed against the real binary: dismissed a
  finding, added an unrelated skill, reran - the dismissal held). `SummaryFor` (used by both the CLI
  and MCP's display fallback) reads that field to rebuild the correct wording from stored evidence,
  not just the in-process Proposal text. `knownSkills` matches both a skill's frontmatter name and its
  directory name (a `[[link]]` author references what they invoke it as, which can drift from its own
  frontmatter), and only `KindSkill`, not `KindReference` (a flat file, never actually loadable as a
  skill) - both also found by review.

- `broken_link` stayed silent on a `[[link]]` to a real agent or plan, the identical gap #66 fixed for
  skills (#78, generalizing #66) - `asset.DefaultLocations` gives `AgentDirs`/`PlanDirs` the same
  global-plus-per-project shape `SkillDirs` has, and a `[[link]]` never resolves against an agent's or
  a plan's name either. New `asset.DiscoverGlobalAgents`/`DiscoverGlobalPlans`, mirroring
  `DiscoverGlobalSkills`. Took #66's own deferred second review finding seriously this time: the
  classification logic that had drifted into structurally different shapes between `detectBrokenLinks`
  and `SummaryFor` is now one shared `brokenLinkText` function both call, so a fourth and fifth branch
  could not repeat that drift. `target_is_skill` stays untouched rather than being renamed to a shared
  `target_kind` field (#78's own original suggestion, reconsidered against #66's own hash-stability
  lesson) - new, parallel `target_is_agent`/`target_is_plan` booleans instead, each set only when true.
  Verified against the real binary: a memory file linking to a real agent and a real plan, both
  flagged with kind-specific wording, alongside a genuinely unwritten reference that stayed silent.
  `/code-review high` found four more real issues, all fixed: a name existing as more than one kind
  now deterministically picks the first-registered one, not whichever call happened to run last; a
  new `asset.Asset.OnDiskName` field lets `linkTargetNames` reuse the on-disk name callers already
  compute instead of re-deriving it from a path; a missing `brokenLinkKindNoun` entry now falls back
  to the raw kind string instead of rendering a malformed sentence; and a stale paragraph in
  `docs/design.md` asserting #78 still open (left over from #66's own write-up) is corrected.

- `broken_link` flagged a `[[link]]` to a slug that doesn't exist anywhere yet as a defect (#67), which
  Claude Code's own memory convention explicitly permits as a forward reference ("it marks something
  worth writing later, not an error"). Measured by hand across this machine's 16 real memory stores:
  185 of 214 broken_link findings (86%) were exactly this class. `detectBrokenLinks` now also builds a
  global slug index across every store and only reports a link whose target exists somewhere but not
  in the same store - a real, actionable scoping problem, not a permitted forward reference. Changes
  #65's own arithmetic: `broken_link` shrank from 214 findings to roughly 29 on the corpus that
  motivated it, no longer filling the pending cap on its own.

- `loom propose`'s promotion-rule detectors could starve each other out of the pending-proposal cap
  (#65, found on the AMC trial's real corpus) - `detectBrokenLinks` ran first, found 15 broken links,
  filled all 20 slots, and `detectFilenameSlugDrift`/`detectUnreachableAssets` never stored a single
  proposal even when they held the actual fix (6 of those 15 broken links were files that exist, under
  a name that only fails on a filename/frontmatter separator drift). `Store` now interleaves every
  proposal by kind, round-robin, before filling the cap, so no kind can exhaust it before every other
  kind with real findings gets a fair share; `GenerateMemoryFindings` also reordered so root-cause
  kinds run before the symptom kind. `[[MEMORY]]` could also never resolve, since the store's own index
  is deliberately excluded from the file set the checks run against - new `asset.MemoryIndexSlug`
  special-cases it. A related finding (a link to a real skill, not a memory, gets a broken-link
  rationale that isn't quite true) deferred - needs cross-project skill discovery, which doesn't exist.

- A file whose size never changed since before a new ingest feature shipped never gained that
  feature's data (#49) - `NeedsIngest` only ever asked "has this file's size changed", so B7b/B7a's
  `tool_usage`/`compactions`/`asset_usage` tables silently never backfilled onto anything already
  ingested before those tables existed, confirmed against this machine's own real ledger. New
  `runs.feature_version` column, stamped by `InsertRun` on every write against a new
  `ledger.CurrentFeatureVersion` constant; `NeedsIngest` now also re-reads a run stored below the
  current value, regardless of size. `ADD COLUMN ... DEFAULT 0` backfills every existing row below
  the current version automatically, so upgrading to this version re-ingests everything once and
  self-heals the exact gap #49 found, not just future ones.

- A pending proposal was never withdrawn when its evidence stopped holding (#40) - `UpsertProposal`'s
  dedupe rule only ever inserted, replaced, or left alone, so a proposal stayed pending, quoting
  stale numbers, until a human dismissed something that was never wrong so much as out of date. New
  `ProposalWithdrawn` status, distinct from dismissed (a different event: the generator changed its
  mind, not a human). `Store` now withdraws stale pending rows before its own upsert loop, which lets
  a genuinely new proposal use the slot a withdrawn one just freed in the same pass, since
  `MaxPendingProposals` counts pending rows only. `Apply` refuses a withdrawn proposal the same way it
  already refused an applied one.

  Reviewed before merge, and three real bugs survived until it was: the "evidence unchanged, leave
  alone" fast path didn't distinguish withdrawn from dismissed, so a proposal with static evidence
  (the B7c memory-finding kinds) could never return to pending once withdrawn - fixed by treating a
  withdrawn row like "no row" for that check, so it revives; the revival itself then needed the same
  pending-cap check a brand new proposal gets, or a burst of revivals could silently exceed it; and
  `WithdrawStalePending`'s dedupe key was a `"kind|subject"` string concatenation, not collision-safe
  against a filesystem path containing `|` - replaced with a struct key, and its per-row updates moved
  into one transaction. A fourth, lower-severity finding (a transiently-unreadable memory store can
  cause a one-pass withdrawal flicker) filed as #59 rather than fixed here - self-healing after the
  fix above, and a real fix needs a design decision this PR shouldn't carry.

- `get_recommendation` serialised an empty `matches` list as JSON `null` rather than `[]`, found by
  calling the live MCP tool for real rather than trusting the test suite - the existing test built
  its expectation by unmarshaling the response back into a Go slice, and `nil` and `[]T{}` unmarshal
  identically, so `len(out.Matches) == 0` passed either way. Fixed by initialising `Matches` to a
  non-nil empty slice; test now also asserts `!= nil`.

- Three bugs in B7c's memory checks, found by running `/code-review high` against the PR before
  merging rather than after - the first time this project has done that. `DiscoverAllMemory`
  propagated any per-store read error other than "missing", so one project with a permission
  problem on its memory directory took every other store's findings down with it, and beyond that
  everything `list_proposals`/`loom propose` return; now a bad store is skipped, not fatal, and
  `MemoryIndex` dropped its `error` return entirely since every failure mode it can hit now
  collapses to the same empty-index answer. `detectMemoryDuplicates` ranged directly over a Go map,
  so its output order was randomized per call - harmless until the total findings exceeded the
  20-proposal cap, at which point which subset of duplicates got a slot depended on map iteration
  order; now sorted by subject before returning. The `topic/SKILL.md` subdirectory memory
  convention (already recognized by the single-project `Discover()` path) was invisible to every
  B7c check, including producing a false `broken_link` report against a target that genuinely
  existed; `DiscoverAllMemory` now mirrors `scanMarkdownDir`'s two-shape handling.

- `list_proposals`'s MCP handler resolved `os.UserHomeDir()` inside the request handler itself,
  which made every test of it non-hermetic - `go test` scanned whichever machine ran the suite's
  own real, private memory files once B7c's generator existed to find something there. Fixed by
  resolving `home` once at server construction (`NewServer(db, home)`) instead of per-request; test
  setup now passes an isolated `t.TempDir()`. Found by the test suite itself failing, the same
  discipline that caught the resumed-session double-count below.

- `tool_usage` and `asset_usage` double-counted every call a resumed session's transcript
  replayed (found by `/code-review high`, not by the tests or dogfooding either table shipped
  with). Both stored one aggregated row per `(run_id, tool_name)`/`(run_id, asset_path)` with no
  per-event identity, unlike `compactions`, which already deduped on `boundary_uuid` for exactly
  this reason. Ordinary `tool_use`/`tool_result` lines turn out to replay the same way
  `compact_boundary` records do - confirmed directly, 325 of 1203 `tool_use` ids shared between one
  real session and its resumed continuation. Fixed by giving both tables the same identity
  `compactions` had from the start: one row per `tool_use_id` (its own globally unique id),
  aggregated at query time instead of write time. A ledger built before this fix is migrated by
  dropping and rebuilding both tables; rebuilt at the next `loom report` (see #49 for the separate,
  pre-existing gap in when that backfill actually happens).

  The same review pass found three more real bugs in the same two features, each fixed with its own
  regression test: `retireStaleAssets` never checked `runs.agent_type` for agent-kind assets,
  so a custom agent invoked constantly via the `Agent` tool could be proposed for retirement as
  "never used"; `loom context` printed nothing about compaction when a lane had compactions but no
  tool-output rows, because one early return covered both sections; `BuildAssetLookup` had no
  `ORDER BY`, so two assets sharing a name resolved to whichever row SQLite returned that call.

- Skill discovery (B7a, #42): a flat `.md` directly in a skills directory is discovered as a new
  `KindReference`, not miscounted as a skill - Claude Code only ever loads `<name>/SKILL.md`. Run
  against this machine's own skills directory: 9 real skills, 1 reference, and the reference turned
  out to be a genuine leftover file next to its own skill directory, not a hypothetical case.

- Retirement could never fire for an asset that exists on disk (B7a, #38). `last_seen` is bumped
  by `UpsertAsset` on every discovery pass, so it reset every time `loom advise` ran and never
  reached the staleness threshold for anything still present - only a file already deleted from disk
  could ever be proposed for retirement. Fixed by using `last_used` (from the new asset_usage
  join) as the staleness clock, falling back to the stable `first_seen` when nothing has used it yet.
  `last_seen` is untouched and keeps answering its own question, whether the asset is on disk.

- The privacy-verification test the design doc's "Verification" section has called for since B1 -
  "ingest a fixture containing a planted secret, then grep the database for it" - did not exist
  anywhere in the repo. Added (`internal/ledger/privacy_test.go`), generic over the schema so it
  covers every table without needing an update when a later slice adds one.

- Doc and code hygiene (B7e): `internal/mcp/doc.go` and `cmd/loom/main.go`'s usage string said four
  MCP tools and omitted `dismiss_proposal` - there are five. Four citations of the never-write rule
  as constraint 9 corrected to 8 (9 is "asset-derived text is data"). `docs/design.md`'s `Serve`
  section and a Verification bullet described a Unix domain socket and daemon `serve.go` never
  implemented (stdio only, one process per session). 19 em dashes in `docs/design.md` swept to plain
  hyphens.

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
  supports. Two kinds so far - retiring an asset unseen for 90 days, and pinning a model for an
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
  GitHub's Dependabot vulnerability alerts on the repo (a settings toggle, free on private repos,
  confirmed by testing, doesn't require going public).

- Prompt-injection hardening for the asset-recommendation path: `get_recommendation` and
  `loom advise` re-serve name/description text read verbatim from local files, which is an
  indirect-injection surface once it lands back in another agent's context. Description text is
  now length-capped at discovery time, every affected MCP field and tool description says
  explicitly that the value is untrusted data and not an instruction, and a best-effort
  `suspicious` flag (never a filter) surfaces obviously injection-shaped phrasing. Documented as
  design doc constraint 9.

- Asset discovery (`internal/asset`): scans standard Claude Code locations plus the current
  project for skills, agents, plans, hooks, and per-project memory. Tolerant of both the flat
  `name.md` and directory-with-`SKILL.md` conventions, and of files with no frontmatter at all
  (falls back to the first `#` heading as a description).
- Selector (`internal/selector`): transparent word-overlap scoring of a free-text task descriptor
  against discovered assets, plus a keyword-based cold-start model/effort recommendation.
- MCP server over stdio (`internal/mcp`), exposing `query_ledger`, `get_recommendation`,
  `list_proposals`, and `record_outcome`.
- `loom advise <text>` and `loom serve` CLI commands.
- Ingest, ledger, `loom report` (B1).
- Project scaffolding and design documentation.
