package music

import "testing"

func TestParseIntentPlaySong(t *testing.T) {
	tests := []struct {
		name  string
		input string
		query string
	}{
		{name: "wake phrase", input: "Panda play Never Gonna Give You Up", query: "Never Gonna Give You Up"},
		{name: "mention", input: "<@123> can you play Daft Punk One More Time please", query: "Daft Punk One More Time"},
		{name: "queue up", input: "queue up Radiohead Weird Fishes to the queue", query: "Radiohead Weird Fishes"},
		{name: "want to hear", input: "Panda I wanna hear Sade Smooth Operator", query: "Sade Smooth Operator"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			intent, ok := ParseIntent(test.input)
			if !ok {
				t.Fatal("expected music intent")
			}
			if intent.Action != ActionPlay || intent.Query != test.query {
				t.Fatalf("expected play %q, got %#v", test.query, intent)
			}
		})
	}
}

func TestParseIntentControls(t *testing.T) {
	tests := map[string]Action{
		"Panda pause the music": ActionPause,
		"resume":                ActionResume,
		"skip song":             ActionSkip,
		"stop music":            ActionStop,
		"show queue":            ActionQueue,
		"clear the queue":       ActionClear,
		"what's playing":        ActionNow,
		"music controls":        ActionControls,
	}
	for input, action := range tests {
		intent, ok := ParseIntent(input)
		if !ok {
			t.Fatalf("expected intent for %q", input)
		}
		if intent.Action != action {
			t.Fatalf("expected %s for %q, got %s", action, input, intent.Action)
		}
	}
}

func TestParseIntentIgnoresNonMusic(t *testing.T) {
	for _, input := range []string{
		"Panda what games should we play this weekend?",
		"can you explain queueing theory",
		"please help me write a song",
	} {
		if intent, ok := ParseIntent(input); ok {
			t.Fatalf("did not expect intent for %q, got %#v", input, intent)
		}
	}
}
