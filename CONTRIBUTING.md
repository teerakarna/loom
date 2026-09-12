# Contributing

Loom is early — B1 and B2 are done (ingest/ledger/report; artifact discovery, selector, MCP
server), see `docs/design.md` for current status and what's still ahead (B3 onward). The
architecture is still settling, so discussion on an issue before a larger PR is still the most
useful way to contribute.

## Development

- Go (see `go.mod` for the minimum version).
- `go build ./...`, `go vet ./...`, `go test ./...`, and `golangci-lint run ./...` before opening
  a PR — all four run in CI and must be clean.
- No fixture may be derived from a real Claude Code transcript, even redacted — all test fixtures
  under `testdata/` must be synthetic and hand-written. See `docs/design.md`, "Publishability
  rules." This is a hard requirement, not a style preference: the tool's only input is transcripts,
  which on any real machine contain confidential material.

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
