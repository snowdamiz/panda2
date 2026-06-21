package commands

import (
	"strconv"
	"strings"
)

const feedbackPrefix = "p2f"

const (
	feedbackHelpful    = "h"
	feedbackNotHelpful = "n"
	feedbackTooLong    = "l"
	feedbackWrong      = "w"
	feedbackUnsafe     = "u"
)

type FeedbackRequest struct {
	Request  Request
	TargetID uint
	Rating   string
}

func FeedbackButtonID(targetID uint, rating string) string {
	code := feedbackRatingCode(rating)
	if targetID == 0 || code == "" {
		return ""
	}
	return strings.Join([]string{feedbackPrefix, code, strconv.FormatUint(uint64(targetID), 10)}, ":")
}

func RequestFromFeedbackID(id string, base Request) (FeedbackRequest, bool) {
	parts := strings.Split(id, ":")
	if len(parts) != 3 || parts[0] != feedbackPrefix {
		return FeedbackRequest{}, false
	}
	targetID, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil || targetID == 0 {
		return FeedbackRequest{}, false
	}
	rating := feedbackRating(parts[1])
	if rating == "" {
		return FeedbackRequest{}, false
	}
	return FeedbackRequest{Request: base, TargetID: uint(targetID), Rating: rating}, true
}

func feedbackRatingCode(rating string) string {
	switch strings.ToLower(strings.TrimSpace(rating)) {
	case "helpful", feedbackHelpful:
		return feedbackHelpful
	case "not_helpful", feedbackNotHelpful:
		return feedbackNotHelpful
	case "too_long", feedbackTooLong:
		return feedbackTooLong
	case "wrong", feedbackWrong:
		return feedbackWrong
	case "unsafe", feedbackUnsafe:
		return feedbackUnsafe
	default:
		return ""
	}
}

func feedbackRating(code string) string {
	switch code {
	case feedbackHelpful:
		return "helpful"
	case feedbackNotHelpful:
		return "not_helpful"
	case feedbackTooLong:
		return "too_long"
	case feedbackWrong:
		return "wrong"
	case feedbackUnsafe:
		return "unsafe"
	default:
		return ""
	}
}
