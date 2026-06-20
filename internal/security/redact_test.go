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

func TestRedactSecretsKeepsSnakeCaseIdentifiers(t *testing.T) {
	content := "`discord_summarize_recent_activity` and `discord_fetch_thread_context`"
	got := RedactSecrets(content)
	if strings.Contains(got, "[redacted]") {
		t.Fatalf("tool identifiers should not be redacted: %s", got)
	}
}

func TestRedactSecretsRedactsExplicitAndTokenLikeSecrets(t *testing.T) {
	for _, content := range []string{
		"sk-abcdefghijklmnopqrstuvwxyz123456",
		"ghp_abcdefghijklmnopqrstuvwxyz123456",
		"AKIA1234567890ABCDEF",
		"token abcdefghijklmnop.1234567890abcdef+/",
		"token AbCdEfGhIjKlMnOpQrStUvWxYz123456",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijklmnopqrstuvwxyz",
	} {
		if got := RedactSecrets(content); !strings.Contains(got, "[redacted]") {
			t.Fatalf("expected secret redaction for %q, got %q", content, got)
		}
	}
}

func TestRedactSecretsRedactsLabeledCredentialsAndPrivateKeys(t *testing.T) {
	privateKey := "-----BEGIN PRIVATE KEY-----\nabcdefghijklmnopqrstuvwxyz123456\n-----END PRIVATE KEY-----"
	content := "password=hunter2 client_secret: abc123 " + privateKey
	got := RedactSecrets(content)
	for _, leaked := range []string{"hunter2", "abc123", "abcdefghijklmnopqrstuvwxyz123456"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("expected %q to be redacted from %q", leaked, got)
		}
	}
	if strings.Count(got, "[redacted]") < 3 {
		t.Fatalf("expected redaction markers, got %q", got)
	}
}

func TestSanitizeDiscordContentDoesNotTruncate(t *testing.T) {
	content := strings.Repeat("long ", 500)
	got := SanitizeDiscordContent(content)
	if strings.Contains(got, "[truncated]") || len(got) != len(strings.TrimSpace(content)) {
		t.Fatalf("sanitize should preserve long content for transport splitting, got length %d", len(got))
	}
}
