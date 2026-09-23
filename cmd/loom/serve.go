package main

import (
	"context"
	"os"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teerakarna/loom/internal/ledger"
	"github.com/teerakarna/loom/internal/mcp"
)

// runServe starts Loom's MCP server on stdio and blocks until the client
// disconnects. This is the primary integration surface (docs/design.md,
// "Integration, MCP first"), no separate daemon process, no socket, no
// flags: a client spawns `loom serve` as a subprocess per its own MCP
// server config.
func runServe(_ []string) error {
	ledgerPath, err := defaultLedgerPath()
	if err != nil {
		return err
	}
	db, err := ledger.Open(ledgerPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	server := mcp.NewServer(db, home)
	return server.Run(context.Background(), &gomcp.StdioTransport{})
}
