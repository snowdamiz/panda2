package security

import (
	"regexp"
	"strings"

	"github.com/sn0w/panda2/internal/textutil"
)

var secretLikePattern = regexp.MustCompile(`(?i)(sk-[a-z0-9_-]{12,}|[a-z0-9_./+=-]{32,})`)

func RedactSecrets(value string) string {
	return secretLikePattern.ReplaceAllString(value, "[redacted]")
}

func SafeDiscordContent(value string) string {
	value = RedactSecrets(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "@everyone", "@ everyone")
	value = strings.ReplaceAll(value, "@here", "@ here")
	if len(value) <= 1900 {
		return value
	}
	return textutil.Truncate(value, 1900, "\n\n[truncated]")
}
