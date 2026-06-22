package feedback

import (
	"context"
	"testing"

	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

func TestRecordFeedbackUpsertsAndSummarizes(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	service := NewService(repository.NewFeedbackRepository(db.DB))
	target, err := service.CreateTarget(ctx, TargetRequest{
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserID:    "requester",
		Command:   "ask",
		Content:   "answer",
	})
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	if err := service.Record(ctx, target.ID, "guild-1", "user-1", RatingHelpful, ""); err != nil {
		t.Fatalf("Record helpful: %v", err)
	}
	if err := service.Record(ctx, target.ID, "guild-1", "user-1", RatingWrong, ""); err != nil {
		t.Fatalf("Record wrong: %v", err)
	}
	summary, err := service.Summary(ctx, "guild-1", 0)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if len(summary.Rows) != 1 || summary.Rows[0].Rating != RatingWrong || summary.Rows[0].Count != 1 {
		t.Fatalf("expected upserted wrong feedback, got %+v", summary.Rows)
	}
}
