// Package mcp is the primary integration surface: an MCP server over stdio
// exposing get_recommendation, get_cost_summary, get_context_occupancy and
// list_proposals. See docs/design.md ("Integration - MCP first"). Built on
// github.com/modelcontextprotocol/go-sdk.
//
// Four tools, all read-only or advisory - none of them ever writes to a
// human-authored file, constraint 8, and none of them applies or dismisses
// a proposal. dismiss_proposal and record_outcome are CLI-only - see
// cmd/loom and docs/design.md, "MCP surface widened back".
package mcp
