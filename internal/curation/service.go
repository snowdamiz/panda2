package curation

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/billing"
	"github.com/sn0w/panda2/internal/memory"
	"github.com/sn0w/panda2/internal/security"
	"github.com/sn0w/panda2/internal/store"
	"github.com/sn0w/panda2/internal/textutil"
)

type AuditRecorder interface {
	Record(ctx context.Context, event store.AuditEvent) error
}

type Service struct {
	memory  *memory.Service
	audit   AuditRecorder
	billing *billing.Service
	now     func() time.Time
}

type Interaction struct {
	GuildID   string
	ChannelID string
	UserID    string
	MessageID string
	Command   string
	Prompt    string
	Response  string
}

type Result struct {
	Saved    bool
	Document store.KnowledgeDocument
	Reason   string
}

func NewService(memoryService *memory.Service) *Service {
	return &Service{memory: memoryService, now: time.Now}
}

func (s *Service) WithAuditRecorder(recorder AuditRecorder) *Service {
	s.audit = recorder
	return s
}

func (s *Service) WithBilling(billingService *billing.Service) *Service {
	s.billing = billingService
	return s
}

func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

func (s *Service) CurateAssistantInteraction(ctx context.Context, interaction Interaction) (Result, error) {
	if s == nil || s.memory == nil || strings.TrimSpace(interaction.GuildID) == "" {
		return Result{}, nil
	}
	candidate, ok := candidateFromInteraction(interaction)
	if !ok {
		return Result{Reason: "not durable server knowledge"}, nil
	}
	if containsSensitiveContent(candidate.Content) {
		return Result{Reason: "sensitive content skipped"}, nil
	}
	exists, err := s.memory.HasExactContent(ctx, interaction.GuildID, candidate.Content)
	if err != nil || exists {
		return Result{Reason: "duplicate knowledge skipped"}, err
	}
	results, err := s.memory.Search(ctx, interaction.GuildID, candidate.Content, 3)
	if err != nil {
		return Result{}, err
	}
	for _, result := range results {
		if similarKnowledge(result.Content, candidate.Content) {
			return Result{Reason: "near-duplicate knowledge skipped"}, nil
		}
	}
	metadata, _ := json.Marshal(map[string]string{
		"source_type": "assistant_interaction",
		"channel_id":  interaction.ChannelID,
		"message_id":  interaction.MessageID,
		"command":     interaction.Command,
	})
	expiresAt := s.now().UTC().Add(180 * 24 * time.Hour)
	if candidate.Confidence >= 0.9 {
		expiresAt = time.Time{}
	}
	request := memory.AddDocumentRequest{
		GuildID:        interaction.GuildID,
		Title:          candidate.Title,
		Content:        candidate.Content,
		CreatedBy:      interaction.UserID,
		Source:         "auto_curated",
		Confidence:     candidate.Confidence,
		ReasonSaved:    candidate.Reason,
		SourceMetadata: string(metadata),
	}
	if !expiresAt.IsZero() {
		request.ExpiresAt = &expiresAt
	}
	var reservation billing.Reservation
	if s.billing != nil {
		currentBytes, err := s.memory.StorageBytes(ctx, interaction.GuildID)
		if err != nil {
			return Result{}, err
		}
		reservation, err = s.billing.BeginCurrentUsage(ctx, interaction.GuildID, billing.MetricKnowledgeStorageByte, currentBytes, int64(len([]byte(strings.TrimSpace(candidate.Content)))))
		if err != nil {
			return Result{}, err
		}
		defer func() {
			if reservation.ID != "" {
				_ = s.billing.ReleaseUsage(context.Background(), reservation)
			}
		}()
	}
	document, err := s.memory.AddDocument(ctx, request)
	if err != nil {
		return Result{}, err
	}
	if s.billing != nil && reservation.ID != "" {
		_ = s.billing.CommitUsage(ctx, reservation)
		reservation.ID = ""
	}
	s.recordAudit(ctx, interaction, document, candidate)
	return Result{Saved: true, Document: document, Reason: candidate.Reason}, nil
}

func (s *Service) ExpireLowConfidence(ctx context.Context) (int64, error) {
	if s == nil || s.memory == nil {
		return 0, nil
	}
	return s.memory.DisableExpired(ctx, s.now().UTC())
}

type candidate struct {
	Title      string
	Content    string
	Reason     string
	Confidence float64
}

func candidateFromInteraction(interaction Interaction) (candidate, bool) {
	prompt := strings.TrimSpace(security.RedactSecrets(interaction.Prompt))
	response := strings.TrimSpace(security.RedactSecrets(interaction.Response))
	if prompt == "" || response == "" {
		return candidate{}, false
	}
	lower := strings.ToLower(prompt + "\n" + response)
	if personalProfileSignal(lower) || unresolvedSignal(lower) {
		return candidate{}, false
	}
	reason, confidence, ok := durableSignal(prompt, response)
	if !ok {
		return candidate{}, false
	}
	content := durableSummary(prompt, response)
	if content == "" {
		return candidate{}, false
	}
	return candidate{
		Title:      durableTitle(content),
		Content:    content,
		Reason:     reason,
		Confidence: confidence,
	}, true
}

func durableSignal(prompt, response string) (string, float64, bool) {
	lowerPrompt := strings.ToLower(prompt)
	lowerAll := strings.ToLower(prompt + "\n" + response)
	signals := []struct {
		text       string
		reason     string
		confidence float64
	}{
		{"we decided", "confirmed durable decision", 0.95},
		{"decision:", "confirmed durable decision", 0.95},
		{"server rule", "server rule captured", 0.9},
		{"new rule", "server rule captured", 0.9},
		{"policy is", "server policy captured", 0.9},
		{"workflow is", "server workflow captured", 0.85},
		{"going forward", "durable instruction captured", 0.85},
		{"faq", "server FAQ captured", 0.85},
		{"remember that", "explicit durable memory request", 0.8},
		{"for future reference", "future reference captured", 0.8},
	}
	for _, signal := range signals {
		if strings.Contains(lowerPrompt, signal.text) || strings.Contains(lowerAll, signal.text) {
			return signal.reason, signal.confidence, true
		}
	}
	return "", 0, false
}

func durableSummary(prompt, response string) string {
	text := strings.TrimSpace(prompt)
	lower := strings.ToLower(text)
	for _, prefix := range []string{"remember that", "for future reference,", "for future reference", "we decided that", "we decided"} {
		if strings.HasPrefix(lower, prefix) {
			text = strings.TrimSpace(text[len(prefix):])
			break
		}
	}
	if len([]rune(text)) < 24 {
		text = response
	}
	text = strings.Trim(text, " \t\r\n\"'`")
	text = textutil.Truncate(text, 700, "...")
	if text == "" {
		return ""
	}
	return "Auto-curated server knowledge:\n" + text
}

func durableTitle(content string) string {
	content = strings.TrimPrefix(strings.TrimSpace(content), "Auto-curated server knowledge:")
	content = strings.TrimSpace(content)
	if content == "" {
		return "Auto-curated server knowledge"
	}
	firstLine := strings.Split(content, "\n")[0]
	firstLine = strings.Trim(firstLine, " \t\r\n:.-")
	if len([]rune(firstLine)) > 80 {
		firstLine = textutil.Truncate(firstLine, 77, "...")
	}
	return firstNonEmpty(firstLine, "Auto-curated server knowledge")
}

func containsSensitiveContent(content string) bool {
	redacted := security.RedactSecrets(content)
	if redacted != content {
		return true
	}
	lower := strings.ToLower(content)
	for _, word := range []string{"password", "api key", "secret", "token", "private key", "ssn", "social security"} {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

func personalProfileSignal(lower string) bool {
	for _, phrase := range []string{"my favorite", "i prefer", "my preference", "call me", "my birthday", "my address"} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

func unresolvedSignal(lower string) bool {
	for _, phrase := range []string{"maybe", "not sure", "unconfirmed", "speculation", "we might"} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

func similarKnowledge(left, right string) bool {
	left = normalizeComparable(left)
	right = normalizeComparable(right)
	if left == "" || right == "" {
		return false
	}
	return strings.Contains(left, right) || strings.Contains(right, left)
}

func normalizeComparable(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.Join(strings.Fields(value), " ")
	return value
}

func (s *Service) recordAudit(ctx context.Context, interaction Interaction, document store.KnowledgeDocument, candidate candidate) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Record(ctx, store.AuditEvent{
		GuildID:    interaction.GuildID,
		ActorID:    interaction.UserID,
		Action:     "memory.auto_save",
		TargetType: "knowledge_document",
		TargetID:   strconv.FormatUint(uint64(document.ID), 10),
		Metadata: fmt.Sprintf(
			`{"reason":%q,"confidence":%q,"channel_id":%q,"message_id":%q}`,
			candidate.Reason,
			strconv.FormatFloat(candidate.Confidence, 'f', 2, 64),
			interaction.ChannelID,
			interaction.MessageID,
		),
	})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
