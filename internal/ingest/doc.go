// Package ingest reads Claude Code's session transcript JSONL files, streaming
// line by line with per-file byte-offset checkpoints, and follows new sessions
// via fsnotify after the initial retroactive pass. See docs/design.md ("Core
// engine") and docs/transcript-schema.md for the format being read. Not yet
// implemented.
package ingest
