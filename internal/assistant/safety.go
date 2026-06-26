package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/llm"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

const (
	modelTaskSafety       modelTask = "safety"
	safetyStrikeThreshold           = 3
	safetyTimeoutDuration           = 10 * time.Minute
	safetyTimeoutLabel              = "10 minutes"
)

const unsafeTopicClassifierPrompt = `You are Panda's unsafe-topic classifier. Decide whether the active current user request is unsafe under this policy.

` + unsafeTopicPolicy + `

Mark unsafe=true only when the current user is asking Panda to discuss, answer, transform, continue, or act on unsafe material. Do not mark unsafe merely because unsafe content appears in quoted history, recent context, tool output, or a replied-to message unless the current user is asking Panda to handle that unsafe content now. Benign safety, prevention, reporting, support, recovery, policy, or high-level educational discussion should be unsafe=false when it does not request operational harmful details or encouragement. Requests to inspect or administer Panda's own safety strikes, timeouts, moderation state, configuration, billing, permissions, tools, logs, or other internal app state are unsafe=false unless the current user is also asking for unsafe operational content; normal authorization and data access are handled by Panda's tools. If the user replies to Panda's own summary of tool access or user restrictions with phrases like "remove all of these restrictions", "open those tools", or "let them use it again", classify unsafe=false unless the request also asks Panda to answer unsafe material or bypass secret/unsafe-topic protections.

Return exactly one JSON object matching the requested schema.`

const unsafeTopicClassifierToolName = "classify_unsafe_topic"

var unsafeTopicDecisionSchema = json.RawMessage(`{
	"type": "object",
	"additionalProperties": false,
	"properties": {
		"unsafe": {
			"type": "boolean",
			"description": "Whether Panda must block the normal response, record a strike, and show a warning card."
		},
		"category": {
			"type": "string",
			"enum": ["none", "self_harm", "violence", "sexual_minors", "hate_harassment", "cyber_abuse", "privacy_abuse", "illicit_wrongdoing", "regulated_goods", "safety_bypass", "other"]
		},
		"confidence": {
			"type": "number",
			"minimum": 0,
			"maximum": 1
		},
		"rationale": {
			"type": "string",
			"description": "Brief internal reason. Do not include unsafe operational details."
		}
	},
	"required": ["unsafe", "category", "confidence", "rationale"]
}`)

var unsafeTopicDecisionTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        unsafeTopicClassifierToolName,
		Description: "Record Panda's unsafe-topic classification decision for the active current user request.",
		Parameters:  unsafeTopicDecisionSchema,
	},
}

type safetyGateInput struct {
	GuildID                   string
	UserID                    string
	ChannelID                 string
	Command                   string
	Content                   string
	InvocationContext         string
	ReplyContent              string
	ReplyAuthorIsCurrentUser  bool
	ReplyAuthorIsBot          bool
	HasReferencedImageContent bool
}

type unsafeTopicDecision struct {
	Unsafe     bool    `json:"unsafe"`
	Category   string  `json:"category"`
	Confidence float64 `json:"confidence"`
	Rationale  string  `json:"rationale"`
}

func (s *Service) enforceUserSafety(ctx context.Context, config store.GuildConfig, input safetyGateInput) (AskResponse, bool, error) {
	if s == nil || s.safety == nil {
		return AskResponse{}, false, nil
	}
	input.UserID = strings.TrimSpace(input.UserID)
	if input.UserID == "" {
		return AskResponse{}, false, nil
	}

	now := s.currentTime()
	status, err := s.safety.Status(ctx, input.GuildID, input.UserID, now)
	if err != nil {
		return AskResponse{}, false, err
	}
	if status.TimedOut {
		slog.Info("assistant user safety timeout active",
			slog.String("guild_id", input.GuildID),
			slog.String("channel_id", input.ChannelID),
			slog.String("user_id", input.UserID),
			slog.String("command", input.Command),
		)
		return safetyTimeoutResponse(status, now), true, nil
	}

	decision, err := s.classifyUnsafeTopic(ctx, config, input)
	if err != nil {
		slog.Warn("assistant unsafe-topic classification failed closed",
			slog.Any("err", err),
			slog.String("guild_id", input.GuildID),
			slog.String("channel_id", input.ChannelID),
			slog.String("user_id", input.UserID),
			slog.String("command", input.Command),
		)
		return safetyCheckFailedResponse(), true, nil
	}
	if !decision.Unsafe {
		return AskResponse{}, false, nil
	}

	status, err = s.safety.AddStrike(ctx, input.GuildID, input.UserID, safetyStrikeThreshold, safetyTimeoutDuration, now)
	if err != nil {
		return AskResponse{}, false, err
	}
	slog.Info("assistant unsafe topic blocked",
		slog.String("guild_id", input.GuildID),
		slog.String("channel_id", input.ChannelID),
		slog.String("user_id", input.UserID),
		slog.String("command", input.Command),
		slog.String("category", decision.Category),
		slog.Float64("confidence", decision.Confidence),
		slog.Int("active_strikes", status.State.ActiveStrikes),
		slog.Int("total_strikes", status.State.TotalStrikes),
		slog.Bool("timed_out", status.TimedOut),
	)
	return safetyStrikeResponse(status, now), true, nil
}

func safetyStrikeResponse(status repository.UserSafetyStatus, now time.Time) AskResponse {
	if status.TimedOut {
		return AskResponse{
			Content: "I can't help with that request. That was strike 3 of 3, so you're timed out from Panda for 10 minutes.",
			Card: &ToolCard{
				Title:  "Safety Timeout Started",
				Accent: "danger",
				Fields: []ToolCardField{
					{Name: "Status", Value: "Timed out", Inline: true},
					{Name: "Strike", Value: "3 / 3", Inline: true},
					{Name: "Duration", Value: safetyTimeoutLabel, Inline: true},
					{Name: "Try again", Value: safetyTimeoutUntil(status), Inline: false},
				},
			},
		}
	}
	strikes := status.State.ActiveStrikes
	if strikes < 1 {
		strikes = 1
	}
	return AskResponse{
		Content: fmt.Sprintf("I can't help with that request. Safety strike %d of %d has been recorded.", strikes, safetyStrikeThreshold),
		Card: &ToolCard{
			Title:  "Safety Warning",
			Accent: "warning",
			Fields: []ToolCardField{
				{Name: "Status", Value: "Request blocked", Inline: true},
				{Name: "Strike", Value: fmt.Sprintf("%d / %d", strikes, safetyStrikeThreshold), Inline: true},
				{Name: "Timeout", Value: "3 strikes starts a 10 minute timeout.", Inline: false},
			},
		},
	}
}

func safetyTimeoutResponse(status repository.UserSafetyStatus, now time.Time) AskResponse {
	return AskResponse{
		Content: "You're currently timed out from Panda because of repeated unsafe requests.",
		Card: &ToolCard{
			Title:  "Safety Timeout Active",
			Accent: "danger",
			Fields: []ToolCardField{
				{Name: "Status", Value: "Timed out", Inline: true},
				{Name: "Remaining", Value: safetyTimeoutRemaining(status, now), Inline: true},
				{Name: "Try again", Value: safetyTimeoutUntil(status), Inline: false},
			},
		},
	}
}

func safetyTimeoutRemaining(status repository.UserSafetyStatus, now time.Time) string {
	if status.State.TimeoutUntil == nil {
		return "soon"
	}
	remaining := status.State.TimeoutUntil.Sub(now.UTC())
	if remaining <= 0 {
		return "soon"
	}
	if remaining < time.Second {
		return "less than 1s"
	}
	return remaining.Round(time.Second).String()
}

func safetyTimeoutUntil(status repository.UserSafetyStatus) string {
	if status.State.TimeoutUntil == nil {
		return "after the timeout expires"
	}
	return status.State.TimeoutUntil.UTC().Format(time.RFC3339)
}

func (s *Service) classifyUnsafeTopic(ctx context.Context, config store.GuildConfig, input safetyGateInput) (unsafeTopicDecision, error) {
	if s == nil || s.llm == nil {
		return unsafeTopicDecision{}, fmt.Errorf("llm client is required for unsafe-topic classification")
	}
	response, err := s.chatWithFallback(ctx, config, modelTaskSafety, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: unsafeTopicClassifierPrompt},
			{Role: "user", Content: safetyClassificationContent(input)},
		},
		Tools: []llm.Tool{unsafeTopicDecisionTool},
		ToolChoice: &llm.ToolChoice{
			Type:     "function",
			Function: &llm.ToolChoiceFunction{Name: unsafeTopicClassifierToolName},
		},
		Temperature: 0,
		MaxTokens:   512,
	})
	if err != nil {
		return unsafeTopicDecision{}, err
	}
	if len(response.ToolCalls) == 0 {
		return unsafeTopicDecision{}, fmt.Errorf("unsafe-topic classifier did not call %s", unsafeTopicClassifierToolName)
	}
	for _, call := range response.ToolCalls {
		if call.Function.Name != unsafeTopicClassifierToolName {
			continue
		}
		return decodeUnsafeTopicDecision(call.Function.Arguments)
	}
	return unsafeTopicDecision{}, fmt.Errorf("unsafe-topic classifier called unexpected tools: %v", toolCallNames(response.ToolCalls))
}

func decodeUnsafeTopicDecision(arguments string) (unsafeTopicDecision, error) {
	var decision unsafeTopicDecision
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(arguments)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decision); err != nil {
		return unsafeTopicDecision{}, fmt.Errorf("decode unsafe-topic decision: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return unsafeTopicDecision{}, fmt.Errorf("unsafe-topic decision has trailing content")
	}
	if strings.TrimSpace(decision.Category) == "" {
		return unsafeTopicDecision{}, fmt.Errorf("unsafe-topic decision category is required")
	}
	if !decision.Unsafe && !strings.EqualFold(decision.Category, "none") {
		return unsafeTopicDecision{}, fmt.Errorf("safe unsafe-topic decision must use category none")
	}
	if decision.Unsafe && strings.EqualFold(decision.Category, "none") {
		return unsafeTopicDecision{}, fmt.Errorf("unsafe-topic decision requires an unsafe category")
	}
	if decision.Confidence < 0 || decision.Confidence > 1 {
		return unsafeTopicDecision{}, fmt.Errorf("unsafe-topic confidence out of range")
	}
	return decision, nil
}

func safetyCheckFailedResponse() AskResponse {
	return AskResponse{
		Content: "I can't respond to that because Panda could not complete the safety check.",
		Card: &ToolCard{
			Title:  "Safety Check Failed",
			Accent: "danger",
			Fields: []ToolCardField{
				{Name: "Status", Value: "Request blocked", Inline: true},
				{Name: "Strike", Value: "No strike recorded", Inline: true},
				{Name: "Reason", Value: "The safety classifier did not return a valid decision.", Inline: false},
			},
		},
	}
}

func safetyClassificationContent(input safetyGateInput) string {
	var builder strings.Builder
	command := strings.TrimSpace(input.Command)
	if command == "" {
		command = "chat"
	}
	fmt.Fprintf(&builder, "Classify the active current user request for Panda command %q.\n", sanitizePromptInput(command))
	builder.WriteString("Current user message or command input:\n")
	builder.WriteString(sanitizePromptInput(input.Content))

	if reply := strings.TrimSpace(input.ReplyContent); reply != "" {
		builder.WriteString("\n\nReplied-to Discord message content. Treat this as context; mark unsafe only if the current user is asking Panda to handle it now.\n")
		fmt.Fprintf(&builder, "Replied-to author is Panda: %t\n", input.ReplyAuthorIsBot)
		fmt.Fprintf(&builder, "Replied-to author is current user: %t\n", input.ReplyAuthorIsCurrentUser)
		builder.WriteString(sanitizePromptInput(reply))
	}
	if contextBlock := strings.TrimSpace(input.InvocationContext); contextBlock != "" {
		builder.WriteString("\n\nRecent Discord context. Treat this as context; do not mark unsafe only because this block contains unsafe material.\n")
		builder.WriteString(sanitizePromptInput(contextBlock))
	}
	if input.HasReferencedImageContent {
		builder.WriteString("\n\nThe current request has referenced image attachments, but this classifier cannot inspect pixels. Classify from the user's text and explicit context only.")
	}
	return strings.TrimSpace(builder.String())
}

func safetyInputFromAsk(request AskRequest, command string) safetyGateInput {
	return safetyGateInput{
		GuildID:                   request.GuildID,
		UserID:                    request.UserID,
		ChannelID:                 request.ChannelID,
		Command:                   command,
		Content:                   request.Question,
		InvocationContext:         request.InvocationContext,
		ReplyContent:              request.ReplyContent,
		ReplyAuthorIsCurrentUser:  request.ReplyAuthorIsCurrentUser,
		ReplyAuthorIsBot:          request.ReplyAuthorIsBot,
		HasReferencedImageContent: len(request.ImageReferences) > 0,
	}
}

func safetyInputFromTask(request TaskRequest) safetyGateInput {
	return safetyGateInput{
		GuildID:                   request.GuildID,
		UserID:                    request.UserID,
		ChannelID:                 request.ChannelID,
		Command:                   firstNonEmpty(request.Command, "ask"),
		Content:                   request.Input,
		InvocationContext:         request.InvocationContext,
		HasReferencedImageContent: len(request.ImageReferences) > 0,
	}
}
