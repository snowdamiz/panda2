package queue

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

type Handler func(context.Context, store.Job) error

const defaultLease = 45 * time.Minute

type Worker struct {
	jobs       *repository.JobRepository
	workerID   string
	lease      time.Duration
	retryAfter time.Duration
	handlers   map[string]Handler
	now        func() time.Time
	draining   atomic.Bool
}

func NewWorker(jobs *repository.JobRepository, workerID string) *Worker {
	return &Worker{
		jobs:       jobs,
		workerID:   workerID,
		lease:      defaultLease,
		retryAfter: time.Minute,
		handlers:   map[string]Handler{},
		now:        time.Now,
	}
}

func (w *Worker) Register(kind string, handler Handler) {
	w.handlers[kind] = handler
}

func (w *Worker) SetClock(now func() time.Time) {
	w.now = now
}

func (w *Worker) Drain() {
	w.draining.Store(true)
}

func (w *Worker) Resume() {
	w.draining.Store(false)
}

func (w *Worker) IsDraining() bool {
	return w.draining.Load()
}

func (w *Worker) WorkOnce(ctx context.Context, kind string) (bool, error) {
	if w.IsDraining() {
		return false, nil
	}
	job, ok, err := w.jobs.ClaimNext(ctx, kind, w.workerID, w.lease, w.now())
	if err != nil || !ok {
		return ok, err
	}

	handler, ok := w.handlers[job.Kind]
	if !ok {
		err := fmt.Errorf("no handler registered for job kind %s", job.Kind)
		return true, errors.Join(err, w.jobs.Fail(ctx, job.ID, err.Error(), w.retryAfter, w.now()))
	}

	if err := handler(ctx, job); err != nil {
		return true, w.jobs.Fail(ctx, job.ID, err.Error(), w.retryAfter, w.now())
	}
	return true, w.jobs.Complete(ctx, job.ID, w.now())
}

func (w *Worker) Run(ctx context.Context, kind string, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		worked, err := w.WorkOnce(ctx, kind)
		if err != nil {
			return err
		}
		if worked {
			continue
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
