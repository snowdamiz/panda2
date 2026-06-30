package youtube

import (
	"strings"

	"github.com/sn0w/panda2/internal/llm"
)

func clipStructuredResponseWasTruncated(response llm.ChatResponse, err error) bool {
	finishReason := strings.ToLower(strings.TrimSpace(response.FinishReason))
	switch finishReason {
	case "length", "max_tokens", "token_limit":
		return true
	}
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "unexpected eof") || strings.Contains(text, "unexpected end of json input")
}
