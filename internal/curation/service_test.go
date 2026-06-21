package curation

import (
	"context"
	"testing"

	"github.com/sn0w/panda2/internal/memory"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

func TestCurateAssistantInteractionSavesDurableDecision(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	memoryService := memory.NewService(repository.NewKnowledgeRepository(db.DB))
	service := NewService(memoryService)
	result, err := service.CurateAssistantInteraction(ctx, Interaction{
		GuildID:  "guild-1",
		UserID:   "user-1",
		Command:  "chat",
		Prompt:   "We decided that refunds are available within 14 days with a receipt.",
		Response: "Got it.",
	})
	if err != nil {
		t.Fatalf("CurateAssistantInteraction: %v", err)
	}
	if !result.Saved || result.Document.Source != "auto_curated" {
		t.Fatalf("expected saved auto-curated document, got %+v", result)
	}
	docs, err := memoryService.ListDocuments(ctx, "guild-1", 10)
	if err != nil || len(docs) != 1 {
		t.Fatalf("expected one document, docs=%+v err=%v", docs, err)
	}
}

func TestCurateAssistantInteractionSkipsSensitiveContent(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	memoryService := memory.NewService(repository.NewKnowledgeRepository(db.DB))
	service := NewService(memoryService)
	result, err := service.CurateAssistantInteraction(ctx, Interaction{
		GuildID:  "guild-1",
		UserID:   "user-1",
		Command:  "chat",
		Prompt:   "Remember that the API key is sk-live-secret.",
		Response: "I will not store secrets.",
	})
	if err != nil {
		t.Fatalf("CurateAssistantInteraction: %v", err)
	}
	if result.Saved {
		t.Fatalf("sensitive content should not be saved: %+v", result)
	}
}
