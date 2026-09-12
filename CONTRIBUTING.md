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

## Commit style

Conventional commits (`feat:`, `fix:`, `docs:`, `chore:`) where practical.

## Reporting a security issue

See [SECURITY.md](SECURITY.md).
