// Package mcp is the primary integration surface: an MCP server over stdio
// exposing tools to query the ledger, get a task recommendation, list and
// dismiss proposals, and record an outcome. See docs/design.md ("Integration
// - MCP first"). Built on github.com/modelcontextprotocol/go-sdk.
//
// Five tools, all advisory: query_ledger, get_recommendation, list_proposals,
// dismiss_proposal (added in B5b), and record_outcome. None of them write to
// a human-authored file - constraint 8.
package mcp
