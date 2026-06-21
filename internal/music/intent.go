package music

import (
	"regexp"
	"strings"
	"unicode"
)

var leadingMentionPattern = regexp.MustCompile(`^<@!?\d+>\s*[,:\-]?\s*`)

func ParseIntent(content string) (Intent, bool) {
	text := normalizeCommandText(content)
	if text == "" {
		return Intent{}, false
	}
	lower := strings.ToLower(text)

	if isControlsRequest(lower) {
		return Intent{Action: ActionControls}, true
	}
	if isNowPlayingRequest(lower) {
		return Intent{Action: ActionNow}, true
	}
	if isClearQueueRequest(lower) {
		return Intent{Action: ActionClear}, true
	}
	if isQueueStatusRequest(lower) {
		return Intent{Action: ActionQueue}, true
	}
	if query, ok := queueSongQuery(text, lower); ok {
		return Intent{Action: ActionPlay, Query: query}, true
	}
	if isPauseRequest(lower) {
		return Intent{Action: ActionPause}, true
	}
	if isSkipRequest(lower) {
		return Intent{Action: ActionSkip}, true
	}
	if isStopRequest(lower) {
		return Intent{Action: ActionStop}, true
	}
	if isResumeRequest(lower) {
		return Intent{Action: ActionResume}, true
	}
	if query, ok := playSongQuery(text, lower); ok {
		if isBareMusicNoun(query) {
			return Intent{Action: ActionResume}, true
		}
		return Intent{Action: ActionPlay, Query: query}, true
	}
	return Intent{}, false
}

func normalizeCommandText(content string) string {
	text := strings.TrimSpace(content)
	for {
		next := leadingMentionPattern.ReplaceAllString(text, "")
		if next == text {
			break
		}
		text = strings.TrimSpace(next)
	}
	text = stripPandaWake(text)
	text = stripPoliteLeadIn(text)
	return strings.Trim(text, " \t\r\n\"'")
}

func stripPandaWake(text string) string {
	tokens := leadingWordTokens(text, 3)
	if len(tokens) == 0 {
		return text
	}
	removeThrough := -1
	if strings.EqualFold(tokens[0].word, "panda") {
		removeThrough = 0
	} else if len(tokens) >= 2 && isGreetingWord(strings.ToLower(tokens[0].word)) && strings.EqualFold(tokens[1].word, "panda") {
		removeThrough = 1
	}
	if removeThrough < 0 {
		return text
	}
	return strings.TrimLeftFunc(strings.TrimSpace(text[tokens[removeThrough].end:]), commandTrimRune)
}

func stripPoliteLeadIn(text string) string {
	for _, lead := range []string{
		"can you please ",
		"could you please ",
		"would you please ",
		"can you ",
		"could you ",
		"would you ",
		"please ",
		"pls ",
	} {
		if strings.HasPrefix(strings.ToLower(text), lead) {
			return strings.TrimSpace(text[len(lead):])
		}
	}
	return text
}

func isControlsRequest(lower string) bool {
	return lower == "music controls" || lower == "music help" || lower == "what music controls do you have"
}

func isNowPlayingRequest(lower string) bool {
	return lower == "now playing" ||
		lower == "what is playing" ||
		lower == "what's playing" ||
		lower == "what song is playing" ||
		lower == "current song"
}

func isClearQueueRequest(lower string) bool {
	return lower == "clear queue" ||
		lower == "clear the queue" ||
		lower == "empty queue" ||
		lower == "empty the queue"
}

func isQueueStatusRequest(lower string) bool {
	return lower == "queue" ||
		lower == "show queue" ||
		lower == "show the queue" ||
		lower == "music queue" ||
		lower == "what is in the queue" ||
		lower == "what's in the queue" ||
		lower == "whats in the queue"
}

func isPauseRequest(lower string) bool {
	return lower == "pause" ||
		lower == "pause music" ||
		lower == "pause the music" ||
		lower == "pause song" ||
		lower == "pause the song"
}

func isSkipRequest(lower string) bool {
	return lower == "skip" ||
		lower == "skip song" ||
		lower == "skip the song" ||
		lower == "next" ||
		lower == "next song" ||
		lower == "play next"
}

func isStopRequest(lower string) bool {
	return lower == "stop" ||
		lower == "stop music" ||
		lower == "stop the music" ||
		lower == "disconnect" ||
		lower == "leave voice" ||
		lower == "leave voice channel" ||
		lower == "leave the voice channel"
}

func isResumeRequest(lower string) bool {
	return lower == "resume" ||
		lower == "unpause" ||
		lower == "continue" ||
		lower == "resume music" ||
		lower == "resume the music" ||
		lower == "start music" ||
		lower == "start the music" ||
		lower == "play"
}

func queueSongQuery(text, lower string) (string, bool) {
	for _, prefix := range []string{"queue up ", "queue the song ", "queue song ", "queue ", "add "} {
		if strings.HasPrefix(lower, prefix) {
			query := strings.TrimSpace(text[len(prefix):])
			query = trimQueueSuffix(query)
			return cleanSongQuery(query), cleanSongQuery(query) != ""
		}
	}
	return "", false
}

func playSongQuery(text, lower string) (string, bool) {
	for _, prefix := range []string{
		"play me ",
		"play us ",
		"play some ",
		"play the song ",
		"play song ",
		"play ",
		"put on ",
		"listen to ",
		"i want to listen to ",
		"i wanna listen to ",
		"i want to hear ",
		"i wanna hear ",
	} {
		if strings.HasPrefix(lower, prefix) {
			query := strings.TrimSpace(text[len(prefix):])
			return cleanSongQuery(query), true
		}
	}
	return "", false
}

func trimQueueSuffix(query string) string {
	lower := strings.ToLower(query)
	for _, suffix := range []string{" to the queue", " to queue", " into the queue", " into queue"} {
		if strings.HasSuffix(lower, suffix) {
			return strings.TrimSpace(query[:len(query)-len(suffix)])
		}
	}
	return query
}

func cleanSongQuery(query string) string {
	query = strings.Trim(query, " \t\r\n\"'")
	lower := strings.ToLower(query)
	for _, suffix := range []string{" please", " pls"} {
		if strings.HasSuffix(lower, suffix) {
			query = strings.TrimSpace(query[:len(query)-len(suffix)])
			lower = strings.ToLower(query)
		}
	}
	return query
}

func isBareMusicNoun(query string) bool {
	switch strings.ToLower(strings.TrimSpace(query)) {
	case "", "music", "song", "the music", "the song", "it":
		return true
	default:
		return false
	}
}

type wordToken struct {
	word string
	end  int
}

func leadingWordTokens(message string, limit int) []wordToken {
	var tokens []wordToken
	wordStart := -1
	for index, value := range message {
		if value == '_' || unicode.IsLetter(value) || unicode.IsDigit(value) {
			if wordStart < 0 {
				wordStart = index
			}
			continue
		}
		if wordStart >= 0 {
			tokens = append(tokens, wordToken{word: message[wordStart:index], end: index})
			if len(tokens) >= limit {
				return tokens
			}
			wordStart = -1
		}
		if len(tokens) == 0 && !commandTrimRune(value) {
			return tokens
		}
	}
	if wordStart >= 0 {
		tokens = append(tokens, wordToken{word: message[wordStart:], end: len(message)})
	}
	return tokens
}

func isGreetingWord(word string) bool {
	switch word {
	case "hey", "hi", "hello", "yo", "ok", "okay", "please":
		return true
	default:
		return false
	}
}

func commandTrimRune(value rune) bool {
	return value == ',' || value == ':' || value == '-' || unicode.IsSpace(value)
}
