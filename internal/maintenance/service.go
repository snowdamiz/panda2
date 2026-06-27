package maintenance

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

const composedRunRetention = 30 * 24 * time.Hour

type Service struct {
	conversations *repository.ConversationRepository
	attachments   *repository.AttachmentRepository
	composed      *repository.ComposedToolRepository
	store         *store.Store
	removeFile    func(string) error
}

type CleanupStats struct {
	ExpiredConversations  int64
	CleanedAttachments    int
	DeletedComposedRuns   int64
	DeletedComposedDedupe int64
}

func NewService(conversations *repository.ConversationRepository, attachments *repository.AttachmentRepository, store *store.Store) *Service {
	return &Service{
		conversations: conversations,
		attachments:   attachments,
		store:         store,
		removeFile:    os.Remove,
	}
}

func (s *Service) WithRemoveFile(remove func(string) error) *Service {
	s.removeFile = remove
	return s
}

func (s *Service) WithComposedTools(composed *repository.ComposedToolRepository) *Service {
	s.composed = composed
	return s
}

func (s *Service) Cleanup(ctx context.Context, now time.Time) (CleanupStats, error) {
	now = now.UTC()
	expired, err := s.conversations.CloseExpired(ctx, now)
	if err != nil {
		return CleanupStats{}, err
	}

	due, err := s.attachments.DueForCleanup(ctx, now, 100)
	if err != nil {
		return CleanupStats{}, err
	}
	stats := CleanupStats{ExpiredConversations: expired}
	for _, attachment := range due {
		if attachment.TempPath != "" {
			if err := s.removeFile(attachment.TempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return stats, err
			}
		}
		if err := s.attachments.MarkCleanupDone(ctx, attachment.ID, now); err != nil {
			return stats, err
		}
		stats.CleanedAttachments++
	}

	if s.composed != nil {
		deletedRuns, err := s.composed.DeleteRunsBefore(ctx, now.Add(-composedRunRetention))
		if err != nil {
			return stats, err
		}
		stats.DeletedComposedRuns = deletedRuns
		deletedDedupe, err := s.composed.DeleteExpiredDedupe(ctx, now)
		if err != nil {
			return stats, err
		}
		stats.DeletedComposedDedupe = deletedDedupe
	}

	if err := s.store.Optimize(ctx); err != nil {
		return stats, err
	}
	return stats, nil
}
