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
  a PR. All four run in CI and must be clean.
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

## Merging

`main` is protected. A change reaches it through a pull request, never a direct push, and:

- **Five checks must be green**: `test`, `lint`, `govulncheck`, `plugin`, `secrets`. The branch
  must also be up to date with `main` before merging.
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
