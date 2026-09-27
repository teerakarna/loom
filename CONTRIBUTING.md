# Contributing

Loom is pre-1.0 and the architecture is still settling, so discussion on an issue before a larger
PR is the most useful way to contribute.

`docs/design.md` is the file to read first. It carries the current phase, the delivery slices, and
the reasoning behind every design constraint, including the ones that say what Loom will not do.
Status lives there and only there, deliberately, so it cannot drift out of sync with a summary
kept somewhere else.

## Development

- Go (see `go.mod` for the minimum version).
- `go build ./...`, `go vet ./...`, `go test ./...`, and `golangci-lint run ./...` before opening
  a PR. All four run in CI and must be clean. `scripts/ci.sh` runs the same gate in one command
  (plus `govulncheck`, `gitleaks` and the plugin manifest check CI also runs), for ordinary local
  use or for the window CI itself can't run at all (a billing suspension, an outage).
- Run `/code-review` on the diff before opening a PR, sized to the change - low effort for a docs
  fix, high for a new table or schema change. Green CI is necessary, not sufficient: it was still
  green the day `tool_usage` and `asset_usage` shipped double-counting every call a resumed
  session replayed, and `/code-review high` is what found it, run after the fact against code
  already on `main`. Running it before merging, not after, is the whole point.
- No fixture may be derived from a real Claude Code transcript, even redacted. All test fixtures
  under `testdata/` must be synthetic and hand-written, and declared in `testdata/README.md`'s
  manifest. See `docs/design.md`, "Publishability rules". This is a hard requirement, not a style
  preference: the tool's only input is transcripts, which on any real machine contain confidential
  material.

## Local environment

What `.github/workflows/ci.yml` actually pins, so a local run and a CI run can't silently disagree
with no way to tell why - `scripts/ci.sh` is the one command that exercises all of it:

- **golangci-lint**: `v2.13.2`, pinned via the `golangci-lint-action`'s own `version:` input.
- **govulncheck**: `v1.8.0`, pinned in the `go install` command CI runs directly (not the
  `golang/govulncheck-action` - see the comment above that step for why).
- **gitleaks**: the `gitleaks/gitleaks-action` itself is pinned to `v3.0.0`
  (`e0c47f4f8be36e29cdc102c57e68cb5cbf0e8d1e`); it bundles its own gitleaks binary rather than
  taking a separately pinned CLI version, so there is no further version to state here.

`scripts/ci.sh` does not install any of the three itself - golangci-lint, govulncheck and gitleaks
must already be on `PATH` locally, at whatever version is installed, and it prints the version it
finds so a mismatch against the pins above is visible rather than silent. CI does install
govulncheck fresh at the pinned version on every run, since a runner starts with nothing on `PATH`;
golangci-lint and gitleaks arrive through their own actions instead.

## Merging

`main` is protected. A change reaches it through a pull request, never a direct push, and:

- **Two checks must be green**: `ci` (build, vet, test, lint, govulncheck, plugin manifests - one
  job, since each separate job bills at least a full minute regardless of how little it runs) and
  `secrets` (kept split out for its own permission scope). The branch must also be up to date with
  `main` before merging.
- **Commits must be signed.** An unsigned commit anywhere in the branch's history blocks the merge,
  including one made by a CI job that commits back to the branch. Squashing does not wash it out,
  and the error GitHub reports for this is generic, so check signatures first if a PR with green
  checks refuses to merge.
- **Review threads must be resolved**, and these rules apply to administrators too.

## Recording findings

Most of what goes wrong here is found by running the tool against real data, not by tests. That
makes it worth being deliberate about where a finding ends up, because chat history is not a record
and a finding nobody wrote down did not happen.

The test is simple: **will this survive the session somewhere durable?**

| Situation | Where it goes |
|---|---|
| Fixed in the same change | No issue. The PR description and commit message are the record, and a better one, because they carry the fix and the evidence together |
| Found, but deferred | An issue, always. Otherwise it exists only in a conversation nobody will re-read |
| Found, won't fix, or the call belongs to someone else | An issue, for the same reason |
| Recurring, or it should shape future work | A design-doc constraint or a skill, not an issue |

Do not open an issue per observation. A repository that answers "too much to keep track of" by
producing more to keep track of has made the problem worse while appearing to help, which is the
failure constraint 10 exists to prevent.

When a finding does get written up, include the measurement, not the impression. "Cost is
overstated 2.12x, 52.9% of the reported total, worst single file 2.42x" is actionable; "costs look
too high" is not.

## Commit style

Conventional commits (`feat:`, `fix:`, `docs:`, `chore:`) where practical.

## Reporting a security issue

See [SECURITY.md](SECURITY.md).
