package textutil

import (
	"testing"
	"unicode/utf8"
)

func TestTruncateDoesNotSplitUTF8Rune(t *testing.T) {
	got := Truncate("abcédef", 4, "...")
	if got != "abc..." {
		t.Fatalf("unexpected truncated value %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncated value is not valid UTF-8: %q", got)
	}
}

func TestPrefixBytesKeepsWholeRunes(t *testing.T) {
	got := PrefixBytes("abcédef", 5)
	if got != "abcé" {
		t.Fatalf("unexpected prefix %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("prefix is not valid UTF-8: %q", got)
	}
}

func TestSliceBytesAdjustsToRuneBoundaries(t *testing.T) {
	got := SliceBytes("aébc", 2, 4)
	if got != "b" {
		t.Fatalf("unexpected slice %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("slice is not valid UTF-8: %q", got)
	}
}

func TestContainsWordUsesStandaloneWordBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		content string
		word    string
		want    bool
	}{
		{name: "plain", content: "Panda is deploy Friday?", word: "panda", want: true},
		{name: "case insensitive", content: "hey panda, help", word: "PANDA", want: true},
		{name: "hyphenated", content: "red-panda facts", word: "panda", want: true},
		{name: "prefix", content: "pandanotes are ready", word: "panda", want: false},
		{name: "suffix", content: "Ask PandaBot", word: "panda", want: false},
		{name: "empty word", content: "Panda", word: "", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ContainsWord(test.content, test.word); got != test.want {
				t.Fatalf("ContainsWord(%q, %q) = %t; want %t", test.content, test.word, got, test.want)
			}
		})
	}
}
