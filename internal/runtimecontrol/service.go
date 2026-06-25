package runtimecontrol

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sn0w/panda2/internal/repository"
)

const (
	DefaultMaintenanceMessage = "Panda is sleeping, maintenance in progress."
	maxMaintenanceRunes       = 500
)

var ErrMessageTooLong = errors.New("maintenance message is too long")

type Status struct {
	Disabled         bool
	Message          string
	EffectiveMessage string
	UpdatedBy        string
	UpdatedAt        time.Time
}

type SetStatusRequest struct {
	Disabled bool
	Message  string
	Actor    string
}

type Service struct {
	statuses *repository.RuntimeStatusRepository
}

func NewService(statuses *repository.RuntimeStatusRepository) *Service {
	return &Service{statuses: statuses}
}

func (s *Service) Status(ctx context.Context) (Status, error) {
	status, err := s.statuses.Get(ctx)
	if err != nil {
		return Status{}, err
	}
	return statusFromStore(status.Disabled, status.Message, status.UpdatedBy, status.UpdatedAt), nil
}

func (s *Service) SetStatus(ctx context.Context, request SetStatusRequest) (Status, error) {
	message := strings.TrimSpace(request.Message)
	if utf8.RuneCountInString(message) > maxMaintenanceRunes {
		return Status{}, ErrMessageTooLong
	}
	status, err := s.statuses.Update(ctx, request.Disabled, message, request.Actor)
	if err != nil {
		return Status{}, err
	}
	return statusFromStore(status.Disabled, status.Message, status.UpdatedBy, status.UpdatedAt), nil
}

func statusFromStore(disabled bool, message, updatedBy string, updatedAt time.Time) Status {
	message = strings.TrimSpace(message)
	effective := message
	if effective == "" {
		effective = DefaultMaintenanceMessage
	}
	return Status{
		Disabled:         disabled,
		Message:          message,
		EffectiveMessage: effective,
		UpdatedBy:        strings.TrimSpace(updatedBy),
		UpdatedAt:        updatedAt,
	}
}
