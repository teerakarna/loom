// Package ingest reads Claude Code's session transcript JSONL files and
// normalizes each line into an Event. See docs/transcript-schema.md for the
// real (empirically confirmed) field names this parses against - several
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
// task-notification. Only present when Status == "completed" - a failed
// completion has no usage to report. Field names here are snake_case in the
// source text (duration_ms), unlike the camelCase durationMs used elsewhere
// in the transcript - see docs/transcript-schema.md.
type AgentUsage struct {
	SubagentTokens int64
	ToolUses       int64
	DurationMs     int64
}

// TaskNotification is a parsed <task-notification> block. These appear for
// both agent completions and background-command completions; Usage is nil
// for the latter (and for failed agent completions) - that is the real
// discriminator, not the shape of TaskID.
type TaskNotification struct {
	TaskID  string
	Status  string // "completed" | "failed"
	Summary string
	Usage   *AgentUsage
}

// ToolUse is one tool_use content block on an assistant line: the tool
// invoked and the id its matching tool_result (on a later user line) refers
// back to. See docs/transcript-schema.md, "tool_use / tool_result".
type ToolUse struct {
	ID   string
	Name string
}

// ToolResult is one tool_result content block on a user line. Bytes is the
// size of Content exactly as written in the transcript - a measured byte
// count, never a token estimate (docs/design.md, B7b: "bytes are not
// tokens"). ToolUseID resolves back to a tool name via the ToolUse blocks
// seen earlier in the same file; see applyEvent.
type ToolResult struct {
	ToolUseID string
	Bytes     int64
}

// ArtifactTouch is one tool_use block that issue #39 counts as an artifact
// usage signal: a Skill invocation, or a Read/Edit/Write call. ToolUseID is
// the block's own id - the identity a resumed session's replayed history
// gets deduped on at the ledger layer, the same pattern CompactionEvent's
// UUID already uses (confirmed on a real corpus: ordinary tool_use blocks
// are replayed verbatim on resume just like compact_boundary records are -
// found by code review, not by the tests this was first built and shipped
// with). Signal is a skill name (from Skill) or a file path (from
// Read/Edit/Write); resolving it to the artifact it names happens at the
// ledger layer, which is what knows what is currently discovered.
type ArtifactTouch struct {
	ToolUseID string
	Signal    string
}

// CompactionEvent is one compact_boundary system record, read directly off
// the host's own accounting rather than inferred (docs/design.md, B7b).
// UUID is the record's own uuid field - a resumed session's transcript
// replays a prior compaction verbatim, uuid included, and this is the
// identity B7b dedupes on (see docs/transcript-schema.md).
type CompactionEvent struct {
	UUID                    string
	Timestamp               time.Time
	Trigger                 string
	PreTokens               int64
	PostTokens              int64
	CumulativeDroppedTokens int64
	DurationMs              int64
}

// Event is one normalized transcript line. Unrecognized or irrelevant line
// types produce Type == "" and should be skipped by the caller - ingest must
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
	MessageID string
	// Effort is the reasoning effort for this turn ("high", ...), read from
	// the line's top-level "effort" field, not from message. Empty when the
	// line does not carry one, which is common enough that every consumer
	// treats it as optional.
	Effort       string
	DurationMs   int64
	ToolUseCount int // count of tool_use content blocks on this line

	// Populated when this line records a tool being denied or the user
	// correcting a result - both are rework signals per docs/design.md.
	ToolDenialKind string
	UserFeedback   string

	// Populated when this line's content is a <task-notification> block
	// (queue-operation lines, and the "user" lines that echo them).
	TaskNotification *TaskNotification

	// Populated for assistant lines: one entry per tool_use content block, in
	// the order they appear. See docs/transcript-schema.md.
	ToolUses []ToolUse
	// Populated for user lines: one entry per tool_result content block.
	ToolResults []ToolResult
	// Populated for a system line with subtype "compact_boundary".
	Compaction *CompactionEvent

	// SkillInvocations and FileTouches are issue #39's two structured usage
	// signals, both read from a tool_use block's own input field on an
	// assistant line - never from message text. A skill's name and
	// description appear in every session's system prompt whether invoked
	// or not, and a tool_result can echo an artifact's name back as plain
	// content (confirmed on a real corpus: a Read of an unrelated file
	// quoted a skill's name in passing). Counting either would reproduce
	// the near-identical-count-for-every-artifact bug an earlier attempt
	// already hit. See docs/design.md, B7a.
	//
	// SkillInvocations holds a Skill tool_use's "skill" input verbatim -
	// resolving it to the artifact it names happens at the ledger layer,
	// which is what knows what is currently discovered; ingest does not.
	SkillInvocations []ArtifactTouch
	// FileTouches holds a Read/Edit/Write tool_use's "file_path" input.
	// Already a path, so it can be matched against a discovered artifact's
	// Path by exact equality with no resolution step.
	FileTouches []ArtifactTouch
}
