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
