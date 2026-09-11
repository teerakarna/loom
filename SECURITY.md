# Security Policy

## Reporting a Vulnerability

Please report security issues privately using
[GitHub Security Advisories](https://github.com/teerakarna/loom/security/advisories/new) for this
repository, rather than opening a public issue.

If you're unable to use Security Advisories, email 21040807+teerakarna@users.noreply.github.com
with a description of the issue and steps to reproduce.

Please do not disclose the issue publicly until it has been addressed.

## Scope

Loom's core has no network egress by design (see `docs/design.md`, "Design constraints"). The
main areas of security interest are:

- The binary-fetch step in the plugin installer (verification of downloaded release binaries)
- The Unix domain socket server (local-only, mode 0600 — a permissions regression here is a
  real vulnerability)
- Anything that could cause message content (not just derived metrics) to end up in the ledger
