package polls

import "testing"

func TestParseAnswersSplitsCommandFriendlyDelimiters(t *testing.T) {
	answers := ParseAnswers("Red | Blue; Green\nYellow")
	if len(answers) != 4 {
		t.Fatalf("expected 4 answers, got %+v", answers)
	}
	if answers[0].Text != "Red" || answers[3].Text != "Yellow" {
		t.Fatalf("unexpected answers: %+v", answers)
	}
}

func TestNewValidatesDiscordPollLimits(t *testing.T) {
	if _, err := New("Lunch?", []Answer{{Text: "Tacos"}, {Text: "Pizza"}}, 0, false); err != nil {
		t.Fatalf("expected default duration poll to be valid: %v", err)
	}
	if _, err := New("", []Answer{{Text: "Tacos"}, {Text: "Pizza"}}, 1, false); err == nil {
		t.Fatal("expected empty question to fail")
	}
	if _, err := New("Lunch?", []Answer{{Text: "Tacos"}}, 1, false); err == nil {
		t.Fatal("expected single-answer poll to fail")
	}
	if _, err := New("Lunch?", []Answer{{Text: "Tacos"}, {Text: "Pizza"}}, MaxDurationHours+1, false); err == nil {
		t.Fatal("expected overlong duration to fail")
	}
}
