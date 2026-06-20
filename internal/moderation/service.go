package moderation

import (
	"context"

	"github.com/sn0w/panda2/internal/assistant"
)

type Service struct {
	assistant *assistant.Service
}

type Request struct {
	GuildID   string
	UserID    string
	ChannelID string
	Context   string
	SubjectID string
	Tone      string
}

func NewService(assistantService *assistant.Service) *Service {
	return &Service{assistant: assistantService}
}

func (s *Service) Triage(ctx context.Context, request Request) (assistant.AskResponse, error) {
	return s.assistant.CompleteTask(ctx, taskRequest(request, "mod-triage"))
}

func (s *Service) DraftNote(ctx context.Context, request Request) (assistant.AskResponse, error) {
	return s.assistant.CompleteTask(ctx, taskRequest(request, "mod-note"))
}

func (s *Service) RecommendSlowmode(ctx context.Context, request Request) (assistant.AskResponse, error) {
	return s.assistant.CompleteTask(ctx, taskRequest(request, "mod-slowmode"))
}

func (s *Service) RecommendCleanup(ctx context.Context, request Request) (assistant.AskResponse, error) {
	return s.assistant.CompleteTask(ctx, taskRequest(request, "mod-cleanup"))
}

func (s *Service) SummarizeUserHistory(ctx context.Context, request Request) (assistant.AskResponse, error) {
	return s.assistant.CompleteTask(ctx, taskRequest(request, "mod-history"))
}

func taskRequest(request Request, command string) assistant.TaskRequest {
	return assistant.TaskRequest{
		GuildID:   request.GuildID,
		UserID:    request.UserID,
		ChannelID: request.ChannelID,
		Command:   command,
		Input:     request.Context,
		Tone:      request.Tone,
		Detail:    request.SubjectID,
	}
}
