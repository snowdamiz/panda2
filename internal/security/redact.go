package security

import (
	"regexp"
	"strings"

	"github.com/sn0w/panda2/internal/textutil"
)

var privateKeyBlockPattern = regexp.MustCompile(`(?is)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
var labeledSecretPattern = regexp.MustCompile(`(?i)\b(api[_-]?key|authorization|client[_-]?secret|cookie|password|passwd|private[_-]?key|pwd|secret|token)\b(\s*[:=]\s*)([^\s,;]+)`)
var explicitSecretPattern = regexp.MustCompile(`(?i)(sk-[a-z0-9_-]{12,}|gh[pousr]_[a-z0-9_]{20,}|AKIA[0-9A-Z]{16})`)
var jwtPattern = regexp.MustCompile(`[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}`)
var genericSecretLikePattern = regexp.MustCompile(`[A-Za-z0-9_./+=-]{32,}`)
var snakeCaseIdentifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func RedactSecrets(value string) string {
	value = privateKeyBlockPattern.ReplaceAllString(value, "[redacted]")
	value = labeledSecretPattern.ReplaceAllString(value, "$1$2[redacted]")
	value = jwtPattern.ReplaceAllString(value, "[redacted]")
	value = explicitSecretPattern.ReplaceAllString(value, "[redacted]")
	return genericSecretLikePattern.ReplaceAllStringFunc(value, func(candidate string) string {
		if snakeCaseIdentifierPattern.MatchString(candidate) {
			return candidate
		}
		if !looksLikeSecretToken(candidate) {
			return candidate
		}
		return "[redacted]"
	})
}

func SanitizeDiscordContent(value string) string {
	value = RedactSecrets(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "@everyone", "@ everyone")
	value = strings.ReplaceAll(value, "@here", "@ here")
	return value
}

func SafeDiscordContent(value string) string {
	value = SanitizeDiscordContent(value)
	if len(value) <= 1900 {
		return value
	}
	return textutil.Truncate(value, 1900, "\n\n[truncated]")
}

func looksLikeSecretToken(candidate string) bool {
	hasDigit := false
	hasLetter := false
	hasMixedCase := false
	hasLower := false
	hasUpper := false
	hasTokenSeparator := false
	for _, r := range candidate {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r >= 'a' && r <= 'z':
			hasLetter = true
			hasLower = true
		case r >= 'A' && r <= 'Z':
			hasLetter = true
			hasUpper = true
		case strings.ContainsRune("./+=-", r):
			hasTokenSeparator = true
		}
	}
	hasMixedCase = hasLower && hasUpper
	return hasLetter && hasDigit && (hasTokenSeparator || hasMixedCase)
}
