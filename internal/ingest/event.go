// Package ingest reads Claude Code's session transcript JSONL files and
// normalizes each line into an Event. See docs/transcript-schema.md for the
// real (empirically confirmed) field names this parses against — several
// differ from what was originally assumed in docs/design.md.
package ingest

import "time"

// Usage is one assistant turn's token accounting, using the actual field
// names confirmed by inspection (see docs/transcript-schema.md, "assistant
// lines"). CacheCreation splits by TTL because the two bucket types plausibly
// cost differently and must not be summed as one undifferentiated figure.
type Usage struct {
	InputTokens                 int64
	OutputTokens                int64
	CacheReadInputTokens        int64
	CacheCreationInputTokens    int64
	CacheCreationEphemeral1hTok int64
	CacheCreationEphemeral5mTok int64
}

// AgentUsage is the <usage> block from a successful agent completion
// task-notification. Only present when Status == "completed" — a failed
// completion has no usage to report. Field names here are snake_case in the
// source text (duration_ms), unlike the camelCase durationMs used elsewhere
// in the transcript — see docs/transcript-schema.md.
type AgentUsage struct {
	SubagentTokens int64
	ToolUses       int64
	DurationMs     int64
}

// TaskNotification is a parsed <task-notification> block. These appear for
// both agent completions and background-command completions; Usage is nil
// for the latter (and for failed agent completions) — that is the real
// discriminator, not the shape of TaskID.
type TaskNotification struct {
	TaskID  string
	Status  string // "completed" | "failed"
	Summary string
	Usage   *AgentUsage
}

// Event is one normalized transcript line. Unrecognized or irrelevant line
// types produce Type == "" and should be skipped by the caller — ingest must
// never fail on a line it doesn't understand (design doc constraint 3,
// schema-tolerant).
type Event struct {
	Type      string // raw "type" field: "assistant", "user", "queue-operation", ...
	Timestamp time.Time
	SessionID string

	// Populated for assistant lines.
	Model string
	Usage *Usage
	// MessageID is message.id: the id of the API response this line belongs
	// to. One response is written as SEVERAL transcript lines, one per
	// content block (thinking, text, each tool_use), and every one of those
	// lines repeats the same usage object. So usage must be counted once per
	// distinct MessageID, not once per line - see applyEvent, and
	// docs/transcript-schema.md ("One response spans several lines").
	MessageID    string
	DurationMs   int64
	ToolUseCount int // count of tool_use content blocks on this line

	// Populated when this line records a tool being denied or the user
	// correcting a result — both are rework signals per docs/design.md.
	ToolDenialKind string
	UserFeedback   string

	// Populated when this line's content is a <task-notification> block
	// (queue-operation lines, and the "user" lines that echo them).
	TaskNotification *TaskNotification
}
