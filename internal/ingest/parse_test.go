package ingest

import "testing"

func TestParseTaskNotification(t *testing.T) {
	t.Run("successful agent completion has usage", func(t *testing.T) {
		content := `<task-notification>
<task-id>synthagent0001</task-id>
<status>completed</status>
<summary>Agent "Do the synthetic thing" finished</summary>
<result>done</result>
<usage><subagent_tokens>777</subagent_tokens><tool_uses>3</tool_uses><duration_ms>5000</duration_ms></usage>
</task-notification>`
		tn := ParseTaskNotification(content)
		if tn == nil {
			t.Fatal("expected a parsed notification, got nil")
		}
		if tn.TaskID != "synthagent0001" {
			t.Errorf("TaskID = %q, want %q", tn.TaskID, "synthagent0001")
		}
		if tn.Status != "completed" {
			t.Errorf("Status = %q, want %q", tn.Status, "completed")
		}
		if tn.Usage == nil {
			t.Fatal("expected non-nil Usage for a completed agent notification")
		}
		if tn.Usage.SubagentTokens != 777 || tn.Usage.ToolUses != 3 || tn.Usage.DurationMs != 5000 {
			t.Errorf("Usage = %+v, want {777 3 5000}", tn.Usage)
		}
	})

	t.Run("failed agent completion has no usage", func(t *testing.T) {
		content := `<task-notification>
<task-id>synthagent0002</task-id>
<status>failed</status>
<summary>Agent "Do the other thing" failed: something went wrong</summary>
</task-notification>`
		tn := ParseTaskNotification(content)
		if tn == nil {
			t.Fatal("expected a parsed notification, got nil")
		}
		if tn.Status != "failed" {
			t.Errorf("Status = %q, want %q", tn.Status, "failed")
		}
		if tn.Usage != nil {
			t.Errorf("expected nil Usage for a failed completion, got %+v", tn.Usage)
		}
	})

	t.Run("background command completion has no usage", func(t *testing.T) {
		content := `<task-notification>
<task-id>bsynthback01</task-id>
<status>completed</status>
<summary>Background command "sleep 1" completed (exit code 0)</summary>
</task-notification>`
		tn := ParseTaskNotification(content)
		if tn == nil {
			t.Fatal("expected a parsed notification, got nil")
		}
		if tn.Usage != nil {
			t.Errorf("expected nil Usage for a background-command completion, got %+v", tn.Usage)
		}
	})

	t.Run("non-notification content returns nil", func(t *testing.T) {
		if tn := ParseTaskNotification("just some ordinary text"); tn != nil {
			t.Errorf("expected nil for non-notification content, got %+v", tn)
		}
	})
}

func TestParseLineAssistantUsage(t *testing.T) {
	raw := []byte(`{"type":"assistant","sessionId":"s1","timestamp":"2026-01-01T00:00:05Z","durationMs":1200,"message":{"model":"claude-sonnet-5","content":[{"type":"text","text":"ok"},{"type":"tool_use","name":"Bash","input":{}}],"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":20,"cache_creation_input_tokens":10,"cache_creation":{"ephemeral_1h_input_tokens":10,"ephemeral_5m_input_tokens":0}}}}`)
	ev, ok := ParseLine(raw)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if ev.Model != "claude-sonnet-5" {
		t.Errorf("Model = %q", ev.Model)
	}
	if ev.ToolUseCount != 1 {
		t.Errorf("ToolUseCount = %d, want 1", ev.ToolUseCount)
	}
	if ev.DurationMs != 1200 {
		t.Errorf("DurationMs = %d, want 1200", ev.DurationMs)
	}
	if ev.Usage == nil {
		t.Fatal("expected non-nil Usage")
	}
	want := Usage{InputTokens: 100, OutputTokens: 50, CacheReadInputTokens: 20,
		CacheCreationInputTokens: 10, CacheCreationEphemeral1hTok: 10}
	if *ev.Usage != want {
		t.Errorf("Usage = %+v, want %+v", *ev.Usage, want)
	}
}

func TestParseLineUnknownTypeIsSkipped(t *testing.T) {
	// A line type ingest doesn't recognize must not error — schema-tolerant
	// per docs/design.md constraint 3.
	if _, ok := ParseLine([]byte(`{"type":"some-future-line-type","foo":"bar"}`)); !ok {
		t.Error("expected ok=true even for an unrecognized type — it should just carry no useful fields")
	}
	if _, ok := ParseLine([]byte(`not json at all`)); ok {
		t.Error("expected ok=false for genuinely malformed JSON")
	}
	if _, ok := ParseLine([]byte(`{}`)); ok {
		t.Error("expected ok=false for a line with no type field")
	}
}

func TestWeightedCost(t *testing.T) {
	u := Usage{InputTokens: 100, OutputTokens: 10, CacheReadInputTokens: 1000, CacheCreationEphemeral1hTok: 50}
	got := WeightedCost(u)
	want := 100*weightInput + 10*weightOutput + 1000*weightCacheRead + 50*weightCacheWrite1h
	if got != want {
		t.Errorf("WeightedCost = %v, want %v", got, want)
	}
}
