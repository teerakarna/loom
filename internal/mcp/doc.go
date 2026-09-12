// Package mcp is the primary integration surface: an MCP server over stdio
// exposing tools to query the ledger, get a task recommendation, list
// proposals, and record an outcome. See docs/design.md ("Integration — MCP
// first"). Built on github.com/modelcontextprotocol/go-sdk.
//
// B2 scope: four tools, all advisory. list_proposals correctly returns an
// empty list until B5 populates the proposals table — see
// internal/ledger/proposal.go.
package mcp
