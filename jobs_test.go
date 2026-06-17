package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestJobManagerQueuesAndCancelsQueuedJob(t *testing.T) {
	started := make(chan string, 1)
	manager := NewJobManager(1, func(ctx context.Context, job *AgentJob) {
		started <- job.ID
		<-ctx.Done()
	})

	first := manager.Enqueue(context.Background(), &AgentJob{ID: "first", ChatID: 123, Message: "one"})
	if first.State != JobRunning {
		t.Fatalf("first job state = %s, want running", first.State)
	}
	if got := <-started; got != "first" {
		t.Fatalf("started job = %s, want first", got)
	}

	second := manager.Enqueue(context.Background(), &AgentJob{ID: "second", ChatID: 123, Message: "two"})
	if second.State != JobQueued {
		t.Fatalf("second job state = %s, want queued", second.State)
	}

	canceled, err := manager.Cancel(123, "second")
	if err != nil {
		t.Fatalf("Cancel returned error: %v", err)
	}
	if len(canceled) != 1 || canceled[0].ID != "second" || canceled[0].State != JobCanceled {
		t.Fatalf("unexpected canceled jobs: %#v", canceled)
	}

	status := formatJobStatus(manager.Status(123))
	if !strings.Contains(status, "first") || !strings.Contains(status, "second") {
		t.Fatalf("status should include active and canceled history:\n%s", status)
	}
	_, _ = manager.Cancel(123, "first")
}

func TestJobManagerFinishStartsNextQueuedJob(t *testing.T) {
	started := make(chan string, 2)
	manager := NewJobManager(1, func(ctx context.Context, job *AgentJob) {
		started <- job.ID
	})

	firstJob := &AgentJob{ID: "first", ChatID: 123, Message: "one"}
	manager.Enqueue(context.Background(), firstJob)
	manager.Enqueue(context.Background(), &AgentJob{ID: "second", ChatID: 123, Message: "two"})
	if got := <-started; got != "first" {
		t.Fatalf("started job = %s, want first", got)
	}

	manager.Finish(firstJob, JobSucceeded, "default", "")
	if got := <-started; got != "second" {
		t.Fatalf("started job after finish = %s, want second", got)
	}
}

func TestJobManagerDoesNotStartQueuedJobAfterParentContextCanceled(t *testing.T) {
	started := make(chan string, 2)
	manager := NewJobManager(1, func(ctx context.Context, job *AgentJob) {
		started <- job.ID
	})

	firstJob := &AgentJob{ID: "first", ChatID: 123, Message: "one"}
	manager.Enqueue(context.Background(), firstJob)
	if got := <-started; got != "first" {
		t.Fatalf("started job = %s, want first", got)
	}

	queuedCtx, cancel := context.WithCancel(context.Background())
	manager.Enqueue(queuedCtx, &AgentJob{ID: "second", ChatID: 123, Message: "two"})
	cancel()

	manager.Finish(firstJob, JobSucceeded, "default", "")
	select {
	case got := <-started:
		t.Fatalf("canceled queued job should not start, got %s", got)
	case <-time.After(50 * time.Millisecond):
	}

	status := manager.Status(123)
	var found bool
	for _, job := range status {
		if job.ID == "second" {
			found = true
			if job.State != JobCanceled {
				t.Fatalf("second job state = %s, want canceled", job.State)
			}
		}
	}
	if !found {
		t.Fatalf("status should include canceled queued job: %#v", status)
	}
}
