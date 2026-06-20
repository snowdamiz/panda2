package memory

import (
	"context"
	"testing"

	"github.com/sn0w/panda2/internal/llm"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

type fakeEmbedder struct {
	requests []llm.EmbeddingRequest
	response llm.EmbeddingResponse
	err      error
}

func (f *fakeEmbedder) Embed(_ context.Context, request llm.EmbeddingRequest) (llm.EmbeddingResponse, error) {
	f.requests = append(f.requests, request)
	return f.response, f.err
}

func TestAddDocumentStoresEmbeddingsWhenConfigured(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	embedder := &fakeEmbedder{response: llm.EmbeddingResponse{
		Model: "openai/text-embedding-3-small",
		Embeddings: []llm.Embedding{
			{Index: 0, Vector: []float64{0.1, 0.2}},
			{Index: 1, Vector: []float64{0.3, 0.4}},
		},
	}}
	service := NewServiceWithEmbeddings(repository.NewKnowledgeRepository(db.DB), embedder, "openai/text-embedding-3-small")

	document, err := service.AddDocument(ctx, AddDocumentRequest{
		GuildID:   "guild-1",
		Title:     "Runbook",
		Content:   "First chunk has deployment notes.\n\n" + longText("Second chunk has rollback notes. ", 70),
		CreatedBy: "admin",
	})
	if err != nil {
		t.Fatalf("AddDocument: %v", err)
	}
	if document.ID == 0 {
		t.Fatal("expected document id")
	}
	if len(embedder.requests) != 1 {
		t.Fatalf("expected one embedding request, got %d", len(embedder.requests))
	}
	if embedder.requests[0].Model != "openai/text-embedding-3-small" || len(embedder.requests[0].Input) != 2 {
		t.Fatalf("unexpected embedding request: %+v", embedder.requests[0])
	}

	var count int64
	if err := db.DB.Table("knowledge_embeddings").Count(&count).Error; err != nil {
		t.Fatalf("count embeddings: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected two embeddings, got %d", count)
	}
}

func TestAddDocumentWithoutEmbedderUsesSearchFallback(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	service := NewService(repository.NewKnowledgeRepository(db.DB))
	if _, err := service.AddDocument(ctx, AddDocumentRequest{
		GuildID:   "guild-1",
		Title:     "Deploy notes",
		Content:   "Production deploys happen on Fridays after review.",
		CreatedBy: "admin",
	}); err != nil {
		t.Fatalf("AddDocument: %v", err)
	}
	results, err := service.Search(ctx, "guild-1", "Friday deploys", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 || results[0].Title != "Deploy notes" {
		t.Fatalf("expected search result, got %+v", results)
	}
}

func longText(value string, count int) string {
	result := ""
	for i := 0; i < count; i++ {
		result += value
	}
	return result
}
