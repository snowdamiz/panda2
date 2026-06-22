package feedback

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

const (
	RatingHelpful    = "helpful"
	RatingNotHelpful = "not_helpful"
	RatingTooLong    = "too_long"
	RatingWrong      = "wrong"
	RatingUnsafe     = "unsafe"
)

type Service struct {
	repo *repository.FeedbackRepository
	now  func() time.Time
}

type TargetRequest struct {
	GuildID   string
	ChannelID string
	UserID    string
	Command   string
	Content   string
	Metadata  map[string]string
}

type Summary struct {
	Rows []repository.FeedbackSummaryRow
}

func NewService(repo *repository.FeedbackRepository) *Service {
	return &Service{repo: repo, now: time.Now}
}

func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

func (s *Service) CreateTarget(ctx context.Context, request TargetRequest) (store.FeedbackTarget, error) {
	if s == nil || s.repo == nil {
		return store.FeedbackTarget{}, fmt.Errorf("feedback service is not configured")
	}
	metadata, err := json.Marshal(request.Metadata)
	if err != nil {
		return store.FeedbackTarget{}, err
	}
	return s.repo.CreateTarget(ctx, store.FeedbackTarget{
		GuildID:     request.GuildID,
		ChannelID:   request.ChannelID,
		UserID:      request.UserID,
		Command:     request.Command,
		ContentHash: contentHash(request.Content),
		Metadata:    string(metadata),
		CreatedAt:   s.now().UTC(),
	})
}

func (s *Service) Record(ctx context.Context, targetID uint, guildID, userID, rating, reason string) error {
	if s == nil || s.repo == nil {
		return fmt.Errorf("feedback service is not configured")
	}
	rating = normalizeRating(rating)
	if rating == "" {
		return fmt.Errorf("unsupported feedback rating")
	}
	target, ok, err := s.repo.Target(ctx, targetID)
	if err != nil {
		return err
	}
	if !ok {
		return repository.ErrNotFound
	}
	if strings.TrimSpace(guildID) != "" && target.GuildID != guildID {
		return repository.ErrNotFound
	}
	_, err = s.repo.Record(ctx, store.FeedbackEvent{
		TargetID: targetID,
		GuildID:  target.GuildID,
		UserID:   userID,
		Rating:   rating,
		Reason:   reason,
	})
	return err
}

func (s *Service) Summary(ctx context.Context, guildID string, window time.Duration) (Summary, error) {
	var since time.Time
	if window > 0 {
		since = s.now().UTC().Add(-window)
	}
	rows, err := s.repo.Summary(ctx, guildID, since)
	return Summary{Rows: rows}, err
}

func normalizeRating(rating string) string {
	switch strings.ToLower(strings.TrimSpace(rating)) {
	case "h", RatingHelpful:
		return RatingHelpful
	case "n", RatingNotHelpful:
		return RatingNotHelpful
	case "l", RatingTooLong:
		return RatingTooLong
	case "w", RatingWrong:
		return RatingWrong
	case "u", RatingUnsafe:
		return RatingUnsafe
	default:
		return ""
	}
}

func contentHash(content string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(content)))
	return hex.EncodeToString(sum[:])
}
