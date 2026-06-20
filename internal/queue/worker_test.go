package queue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

func TestWorkerCompletesClaimedJob(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	jobs := repository.NewJobRepository(db.DB)
	job, err := jobs.Enqueue(ctx, store.Job{Kind: "fixture", Payload: "{}"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	worker := NewWorker(jobs, "worker-1")
	worker.SetClock(func() time.Time { return time.Now().UTC().Add(time.Hour) })
	worker.Register("fixture", func(context.Context, store.Job) error { return nil })

	worked, err := worker.WorkOnce(ctx, "")
	if err != nil {
		t.Fatalf("WorkOnce: %v", err)
	}
	if !worked {
		t.Fatal("expected worker to claim a job")
	}

	var saved store.Job
	if err := db.DB.First(&saved, job.ID).Error; err != nil {
		t.Fatalf("lookup job: %v", err)
	}
	if saved.Status != "succeeded" {
		t.Fatalf("expected succeeded job, got %+v", saved)
	}
}

func TestWorkerRetriesFailedJob(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	jobs := repository.NewJobRepository(db.DB)
	job, err := jobs.Enqueue(ctx, store.Job{Kind: "fixture", MaxAttempts: 2})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	worker := NewWorker(jobs, "worker-1")
	worker.SetClock(func() time.Time { return time.Now().UTC().Add(time.Hour) })
	worker.Register("fixture", func(context.Context, store.Job) error { return errors.New("temporary") })

	worked, err := worker.WorkOnce(ctx, "")
	if err != nil {
		t.Fatalf("WorkOnce: %v", err)
	}
	if !worked {
		t.Fatal("expected worker to claim a job")
	}

	var saved store.Job
	if err := db.DB.First(&saved, job.ID).Error; err != nil {
		t.Fatalf("lookup job: %v", err)
	}
	if saved.Status != "queued" || saved.LastError != "temporary" {
		t.Fatalf("expected queued retry, got %+v", saved)
	}
}

func TestWorkerDrainSkipsJobs(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	jobs := repository.NewJobRepository(db.DB)
	if _, err := jobs.Enqueue(ctx, store.Job{Kind: "fixture"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	worker := NewWorker(jobs, "worker-1")
	worker.Register("fixture", func(context.Context, store.Job) error { return nil })
	worker.Drain()

	worked, err := worker.WorkOnce(ctx, "")
	if err != nil {
		t.Fatalf("WorkOnce: %v", err)
	}
	if worked {
		t.Fatal("draining worker should not claim jobs")
	}
	depth, err := jobs.QueueDepth(ctx, "fixture")
	if err != nil {
		t.Fatalf("QueueDepth: %v", err)
	}
	if depth != 1 {
		t.Fatalf("expected queued job to remain, got depth %d", depth)
	}
}
