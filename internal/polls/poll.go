package polls

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	MinAnswers           = 2
	MaxAnswers           = 10
	MaxQuestionRunes     = 300
	MaxAnswerRunes       = 55
	DefaultDurationHours = 24
	MaxDurationHours     = 32 * 24
)

type Poll struct {
	Question         string
	Answers          []Answer
	DurationHours    int
	AllowMultiselect bool
}

type Answer struct {
	Text  string
	Emoji string
}

func New(question string, answers []Answer, durationHours int, allowMultiselect bool) (Poll, error) {
	poll := Poll{
		Question:         strings.TrimSpace(question),
		Answers:          normalizeAnswers(answers),
		DurationHours:    durationHours,
		AllowMultiselect: allowMultiselect,
	}
	if poll.DurationHours == 0 {
		poll.DurationHours = DefaultDurationHours
	}
	if err := poll.Validate(); err != nil {
		return Poll{}, err
	}
	return poll, nil
}

func ParseAnswers(raw string) []Answer {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '|' || r == '\n' || r == ';'
	})
	answers := make([]Answer, 0, len(fields))
	for _, field := range fields {
		text := strings.TrimSpace(field)
		if text != "" {
			answers = append(answers, Answer{Text: text})
		}
	}
	return answers
}

func (p Poll) Validate() error {
	if p.Question == "" {
		return fmt.Errorf("question is required")
	}
	if utf8.RuneCountInString(p.Question) > MaxQuestionRunes {
		return fmt.Errorf("question must be no more than %d characters", MaxQuestionRunes)
	}
	if len(p.Answers) < MinAnswers {
		return fmt.Errorf("poll requires at least %d answers", MinAnswers)
	}
	if len(p.Answers) > MaxAnswers {
		return fmt.Errorf("poll can have at most %d answers", MaxAnswers)
	}
	for index, answer := range p.Answers {
		if answer.Text == "" {
			return fmt.Errorf("answer %d is required", index+1)
		}
		if utf8.RuneCountInString(answer.Text) > MaxAnswerRunes {
			return fmt.Errorf("answer %d must be no more than %d characters", index+1, MaxAnswerRunes)
		}
	}
	if p.DurationHours < 1 || p.DurationHours > MaxDurationHours {
		return fmt.Errorf("duration_hours must be between 1 and %d", MaxDurationHours)
	}
	return nil
}

func normalizeAnswers(answers []Answer) []Answer {
	normalized := make([]Answer, 0, len(answers))
	for _, answer := range answers {
		text := strings.TrimSpace(answer.Text)
		if text == "" {
			continue
		}
		normalized = append(normalized, Answer{
			Text:  text,
			Emoji: strings.TrimSpace(answer.Emoji),
		})
	}
	return normalized
}
