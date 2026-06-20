package security

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSafeDiscordContentTruncatesUTF8Safely(t *testing.T) {
	content := "x" + strings.Repeat("界", 700)
	got := SafeDiscordContent(content)
	if !strings.Contains(got, "[truncated]") {
		t.Fatalf("expected truncation marker, got %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("safe Discord content is not valid UTF-8")
	}
}
