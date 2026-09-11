# Claude Code plugin packaging

Ships the MCP server registration, optional hooks, the Advisor skill, and a manifest. Plugins are
cloned content and cannot carry a compiled binary, so the install step fetches the matching
platform binary and verifies it — refusing an unverified one rather than falling back. See
`docs/design.md`, "Distribution". Not yet implemented — this lands in phase B6.
