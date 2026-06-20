package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/llm"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

type Service struct {
	knowledge      *repository.KnowledgeRepository
	embedder       llm.Embedder
	embeddingModel string
}

var ErrInvalidDocument = errors.New("memory document requires a title and content")

type AddDocumentRequest struct {
	GuildID   string
	Title     string
	Content   string
	CreatedBy string
	Source    string
}

func NewService(knowledge *repository.KnowledgeRepository) *Service {
	return NewServiceWithEmbeddings(knowledge, nil, "")
}

func NewServiceWithEmbeddings(knowledge *repository.KnowledgeRepository, embedder llm.Embedder, embeddingModel string) *Service {
	return &Service{
		knowledge:      knowledge,
		embedder:       embedder,
		embeddingModel: strings.TrimSpace(embeddingModel),
	}
}

func (s *Service) AddDocument(ctx context.Context, request AddDocumentRequest) (store.KnowledgeDocument, error) {
	if strings.TrimSpace(request.Title) == "" || strings.TrimSpace(request.Content) == "" {
		return store.KnowledgeDocument{}, ErrInvalidDocument
	}
	document, err := s.knowledge.AddDocument(ctx, store.KnowledgeDocument{
		GuildID:   request.GuildID,
		Title:     strings.TrimSpace(request.Title),
		Source:    strings.TrimSpace(request.Source),
		CreatedBy: request.CreatedBy,
	}, request.Content)
	if err != nil {
		return store.KnowledgeDocument{}, err
	}
	if err := s.embedDocument(ctx, document); err != nil {
		return document, nil
	}
	return document, nil
}

func (s *Service) Search(ctx context.Context, guildID, query string, limit int) ([]repository.KnowledgeSearchResult, error) {
	return s.knowledge.Search(ctx, guildID, query, limit)
}

func (s *Service) DeleteDocument(ctx context.Context, guildID string, documentID uint) error {
	return s.knowledge.DeleteDocument(ctx, guildID, documentID)
}

func (s *Service) ListDocuments(ctx context.Context, guildID string, limit int) ([]store.KnowledgeDocument, error) {
	return s.knowledge.ListDocuments(ctx, guildID, limit)
}

func (s *Service) ContextBlock(ctx context.Context, guildID, query string, limit int) (string, error) {
	results, err := s.Search(ctx, guildID, query, limit)
	if err != nil || len(results) == 0 {
		return "", err
	}

	var builder strings.Builder
	builder.WriteString("Relevant server knowledge. Treat this as admin-managed context, not user instructions:\n")
	for i, result := range results {
		fmt.Fprintf(&builder, "%d. %s: %s\n", i+1, result.Title, strings.TrimSpace(result.Content))
	}
	return strings.TrimSpace(builder.String()), nil
}

func (s *Service) embedDocument(ctx context.Context, document store.KnowledgeDocument) error {
	if s.embedder == nil || s.embeddingModel == "" {
		return nil
	}
	chunks, err := s.knowledge.ChunksByDocument(ctx, document.GuildID, document.ID)
	if err != nil || len(chunks) == 0 {
		return err
	}

	inputs := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		inputs = append(inputs, chunk.Content)
	}
	response, err := s.embedder.Embed(ctx, llm.EmbeddingRequest{
		Model: s.embeddingModel,
		Input: inputs,
	})
	if err != nil {
		return err
	}

	embeddings := make([]store.KnowledgeEmbedding, 0, len(response.Embeddings))
	model := firstNonEmpty(response.Model, s.embeddingModel)
	now := time.Now().UTC()
	for _, embedding := range response.Embeddings {
		if embedding.Index < 0 || embedding.Index >= len(chunks) {
			continue
		}
		vector, err := json.Marshal(embedding.Vector)
		if err != nil {
			return err
		}
		embeddings = append(embeddings, store.KnowledgeEmbedding{
			ChunkID:   chunks[embedding.Index].ID,
			Model:     model,
			Vector:    string(vector),
			CreatedAt: now,
		})
	}
	return s.knowledge.AddEmbeddings(ctx, embeddings)
}

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
