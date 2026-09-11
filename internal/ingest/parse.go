package ingest

import (
	"encoding/json"
	"regexp"
	"strconv"
	"time"
)

// ParseLine normalizes one raw JSONL transcript line into an Event. It never
// returns an error for a line it doesn't recognize — unknown or malformed
// lines yield a zero Event with Type == "" and ok == false, which the caller
// should simply skip. This is deliberate (design doc constraint 3,
// schema-tolerant): a new Claude Code version adding line types must not
// break ingest of everything else in the file.
func ParseLine(raw []byte) (Event, bool) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return Event{}, false
	}

	typ, _ := m["type"].(string)
	if typ == "" {
		return Event{}, false
	}

	ev := Event{Type: typ}
	ev.Timestamp = parseTimestamp(m["timestamp"])
	ev.SessionID = firstString(m["sessionId"], m["session_id"])
	ev.ToolDenialKind, _ = m["toolDenialKind"].(string)
	ev.UserFeedback, _ = m["userFeedback"].(string)

	switch typ {
	case "assistant":
		parseAssistant(m, &ev)
	case "queue-operation":
		if content, ok := m["content"].(string); ok {
			ev.TaskNotification = ParseTaskNotification(content)
		}
	case "user":
		// A task-notification is also echoed as a plain-string user message
		// (message.content is a string in that case, not the richer
		// tool_result structure). Only treat it as a notification if it
		// actually parses as one.
		if msg, ok := m["message"].(map[string]any); ok {
			if content, ok := msg["content"].(string); ok {
				if tn := ParseTaskNotification(content); tn != nil {
					ev.TaskNotification = tn
				}
			}
		}
	}

	return ev, true
}

func parseAssistant(m map[string]any, ev *Event) {
	msg, ok := m["message"].(map[string]any)
	if !ok {
		return
	}
	ev.Model, _ = msg["model"].(string)
	if durMs, ok := m["durationMs"]; ok {
		ev.DurationMs = int64(toFloat(durMs))
	}

	if content, ok := msg["content"].([]any); ok {
		for _, block := range content {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if bt, _ := b["type"].(string); bt == "tool_use" {
				ev.ToolUseCount++
			}
		}
	}

	usage, ok := msg["usage"].(map[string]any)
	if !ok {
		return
	}
	u := &Usage{
		InputTokens:              int64(toFloat(usage["input_tokens"])),
		OutputTokens:             int64(toFloat(usage["output_tokens"])),
		CacheReadInputTokens:     int64(toFloat(usage["cache_read_input_tokens"])),
		CacheCreationInputTokens: int64(toFloat(usage["cache_creation_input_tokens"])),
	}
	if cc, ok := usage["cache_creation"].(map[string]any); ok {
		u.CacheCreationEphemeral1hTok = int64(toFloat(cc["ephemeral_1h_input_tokens"]))
		u.CacheCreationEphemeral5mTok = int64(toFloat(cc["ephemeral_5m_input_tokens"]))
	}
	ev.Usage = u
}

// taskNotificationRe extracts the fields ingest actually needs from a
// <task-notification> block. Deliberately not a full XML parser: the
// transcript embeds this as a fixed, simple tag shape (see
// docs/transcript-schema.md), and a small set of targeted regexes is more
// robust to the surrounding text (a <result> block containing arbitrary
// markdown/code, which a strict XML parser could choke on) than a real
// XML/HTML parser would be here.
var (
	taskIDRe   = regexp.MustCompile(`<task-id>([^<]*)</task-id>`)
	statusRe   = regexp.MustCompile(`<status>([^<]*)</status>`)
	summaryRe  = regexp.MustCompile(`<summary>([^<]*)</summary>`)
	usageRe    = regexp.MustCompile(`<usage>(.*?)</usage>`)
	subTokRe   = regexp.MustCompile(`<subagent_tokens>(\d+)</subagent_tokens>`)
	toolUsesRe = regexp.MustCompile(`<tool_uses>(\d+)</tool_uses>`)
	durationRe = regexp.MustCompile(`<duration_ms>(\d+)</duration_ms>`)
)

// ParseTaskNotification extracts a TaskNotification from raw content if it
// looks like one, else returns nil. Usage is only populated when a <usage>
// block is present — absent for background-command completions and for
// failed agent completions, both confirmed by direct inspection (see
// docs/transcript-schema.md).
func ParseTaskNotification(content string) *TaskNotification {
	m := taskIDRe.FindStringSubmatch(content)
	if m == nil {
		return nil
	}
	tn := &TaskNotification{TaskID: m[1]}
	if m := statusRe.FindStringSubmatch(content); m != nil {
		tn.Status = m[1]
	}
	if m := summaryRe.FindStringSubmatch(content); m != nil {
		tn.Summary = m[1]
	}
	if m := usageRe.FindStringSubmatch(content); m != nil {
		block := m[1]
		u := &AgentUsage{}
		if m := subTokRe.FindStringSubmatch(block); m != nil {
			u.SubagentTokens, _ = strconv.ParseInt(m[1], 10, 64)
		}
		if m := toolUsesRe.FindStringSubmatch(block); m != nil {
			u.ToolUses, _ = strconv.ParseInt(m[1], 10, 64)
		}
		if m := durationRe.FindStringSubmatch(block); m != nil {
			u.DurationMs, _ = strconv.ParseInt(m[1], 10, 64)
		}
		tn.Usage = u
	}
	return tn
}

func parseTimestamp(v any) time.Time {
	s, ok := v.(string)
	if !ok {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func firstString(vals ...any) string {
	for _, v := range vals {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// toFloat handles the fact that encoding/json decodes all JSON numbers into
// float64 when unmarshaling into map[string]any.
func toFloat(v any) float64 {
	f, _ := v.(float64)
	return f
}
