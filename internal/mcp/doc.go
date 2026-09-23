// Package mcp is the primary integration surface: an MCP server over stdio
// exposing get_recommendation, a task recommendation tool. See docs/design.md
// ("Integration - MCP first"). Built on github.com/modelcontextprotocol/go-sdk.
//
// One tool, advisory only, and it does not write to a human-authored file -
// constraint 8. Everything else (querying the ledger, listing/dismissing
// proposals, recording an outcome) is CLI-only - see cmd/loom and
// docs/design.md, "MCP server shape, narrowed further".
package mcp
