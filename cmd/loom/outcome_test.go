package main

import "testing"

func TestParseRecordOutcomeArgs(t *testing.T) {
	outcome, detail, taskText, err := parseRecordOutcomeArgs([]string{
		"--outcome", "accepted", "--detail", "no changes needed", "rename", "the", "function",
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != "accepted" || detail != "no changes needed" || taskText != "rename the function" {
		t.Errorf("got outcome=%q detail=%q taskText=%q", outcome, detail, taskText)
	}
}

// A task description that itself contains "--outcome" or "--detail" must not
// be reinterpreted as a flag once the flags (which the usage string requires
// to come first) have ended. Found by /code-review, not by a test that
// existed at the time.
func TestParseRecordOutcomeArgsFlagLikeWordsInTaskText(t *testing.T) {
	outcome, detail, taskText, err := parseRecordOutcomeArgs([]string{
		"--outcome", "accepted",
		"the", "fix", "used", "a", "--outcome", "flag", "by", "mistake",
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != "accepted" {
		t.Errorf("outcome = %q, want accepted", outcome)
	}
	if detail != "" {
		t.Errorf("detail = %q, want empty", detail)
	}
	want := "the fix used a --outcome flag by mistake"
	if taskText != want {
		t.Errorf("taskText = %q, want %q", taskText, want)
	}
}

func TestParseRecordOutcomeArgsRejectsBadOutcome(t *testing.T) {
	if _, _, _, err := parseRecordOutcomeArgs([]string{"--outcome", "maybe", "did a thing"}); err == nil {
		t.Error("want an error for an outcome value that isn't accepted/corrected/rejected")
	}
}

func TestParseRecordOutcomeArgsRequiresTaskText(t *testing.T) {
	if _, _, _, err := parseRecordOutcomeArgs([]string{"--outcome", "accepted"}); err == nil {
		t.Error("want an error when no task text is given")
	}
}
