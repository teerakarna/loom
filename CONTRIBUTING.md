# Contributing

Loom is early — design agreed, build not started (see `docs/design.md`). Until there's a first
working phase (B1: ingest, ledger, `loom report`), the most useful contribution is discussion on
an issue before a PR, since the architecture is still settling.

## Development

- Go (see `go.mod` for the minimum version).
- `go build ./...`, `go vet ./...`, `go test ./...` before opening a PR.
- No fixture may be derived from a real Claude Code transcript, even redacted — all test fixtures
  under `testdata/` must be synthetic and hand-written. See `docs/design.md`, "Publishability
  rules." This is a hard requirement, not a style preference: the tool's only input is transcripts,
  which on any real machine contain confidential material.

## Commit style

Conventional commits (`feat:`, `fix:`, `docs:`, `chore:`) where practical.

## Reporting a security issue

See [SECURITY.md](SECURITY.md).
