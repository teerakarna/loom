package main

import "testing"

func TestParseAdviseArgs(t *testing.T) {
	agentType, text, err := parseAdviseArgs([]string{"--agent-type", "Explore", "find", "the", "bug"})
	if err != nil {
		t.Fatal(err)
	}
	if agentType != "Explore" || text != "find the bug" {
		t.Errorf("got agentType=%q text=%q", agentType, text)
	}
}

func TestParseAdviseArgsNoAgentType(t *testing.T) {
	agentType, text, err := parseAdviseArgs([]string{"find", "the", "bug"})
	if err != nil {
		t.Fatal(err)
	}
	if agentType != "" || text != "find the bug" {
		t.Errorf("got agentType=%q text=%q, want empty agentType", agentType, text)
	}
}

// A task description containing the literal words "--agent-type" must not
// be reinterpreted as a flag once the flags (which must come first) have
// ended - same class of bug as record-outcome's flag parser once had.
func TestParseAdviseArgsFlagLikeWordsInTaskText(t *testing.T) {
	agentType, text, err := parseAdviseArgs([]string{
		"--agent-type", "Explore", "figure", "out", "why", "--agent-type", "flags", "are", "ignored",
	})
	if err != nil {
		t.Fatal(err)
	}
	if agentType != "Explore" {
		t.Errorf("agentType = %q, want Explore", agentType)
	}
	want := "figure out why --agent-type flags are ignored"
	if text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
}

func TestParseAdviseArgsRequiresTaskText(t *testing.T) {
	if _, _, err := parseAdviseArgs([]string{"--agent-type", "Explore"}); err == nil {
		t.Error("want an error when no task text is given")
	}
	if _, _, err := parseAdviseArgs(nil); err == nil {
		t.Error("want an error for no args at all")
	}
}

func TestParseAdviseArgsRequiresAgentTypeValue(t *testing.T) {
	if _, _, err := parseAdviseArgs([]string{"--agent-type"}); err == nil {
		t.Error("want an error when --agent-type has no value")
	}
}
