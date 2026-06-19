package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestJobManagerQueuesAndCancelsQueuedJob(t *testing.T) {
	started := make(chan string, 1)
	manager := NewJobManager(1, func(ctx context.Context, job *AgentJob) {
		started <- job.ID
		<-ctx.Done()
	})

	first, err := manager.Enqueue(context.Background(), &AgentJob{ID: "first", ChatID: 123, Message: "one"})
	if err != nil {
		t.Fatalf("Enqueue first returned error: %v", err)
	}
	if first.State != JobRunning {
		t.Fatalf("first job state = %s, want running", first.State)
	}
	if got := <-started; got != "first" {
		t.Fatalf("started job = %s, want first", got)
	}

	second, err := manager.Enqueue(context.Background(), &AgentJob{ID: "second", ChatID: 123, Message: "two"})
	if err != nil {
		t.Fatalf("Enqueue second returned error: %v", err)
	}
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
	if _, err := manager.Enqueue(context.Background(), firstJob); err != nil {
		t.Fatalf("Enqueue first returned error: %v", err)
	}
	if _, err := manager.Enqueue(context.Background(), &AgentJob{ID: "second", ChatID: 123, Message: "two"}); err != nil {
		t.Fatalf("Enqueue second returned error: %v", err)
	}
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
	if _, err := manager.Enqueue(context.Background(), firstJob); err != nil {
		t.Fatalf("Enqueue first returned error: %v", err)
	}
	if got := <-started; got != "first" {
		t.Fatalf("started job = %s, want first", got)
	}

	queuedCtx, cancel := context.WithCancel(context.Background())
	if _, err := manager.Enqueue(queuedCtx, &AgentJob{ID: "second", ChatID: 123, Message: "two"}); err != nil {
		t.Fatalf("Enqueue second returned error: %v", err)
	}
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

func TestJobManagerLoadsPersistedHistory(t *testing.T) {
	manager := NewJobManager(1, func(ctx context.Context, job *AgentJob) {})
	now := time.Now().UTC()
	manager.LoadHistory([]JobSnapshot{{
		ID:        "old",
		ChatID:    123,
		Message:   "persisted",
		State:     JobSucceeded,
		AgentName: "engineer",
		CreatedAt: now,
	}})

	status := manager.Status(123)
	if len(status) != 1 {
		t.Fatalf("expected one status entry, got %#v", status)
	}
	if status[0].ID != "old" || status[0].State != JobSucceeded || status[0].AgentName != "engineer" {
		t.Fatalf("unexpected restored status: %#v", status[0])
	}
}

func TestJobManagerHistorySinkReceivesFinishedJobs(t *testing.T) {
	var persisted []JobSnapshot
	manager := NewJobManager(1, func(ctx context.Context, job *AgentJob) {})
	manager.SetHistorySink(func(jobs []JobSnapshot) {
		persisted = jobs
	})

	job := &AgentJob{ID: "done", ChatID: 123, Message: "finish me"}
	if _, err := manager.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("Enqueue returned error: %v", err)
	}
	manager.Finish(job, JobSucceeded, "default", "")

	if len(persisted) != 1 {
		t.Fatalf("expected one persisted job, got %#v", persisted)
	}
	if persisted[0].ID != "done" || persisted[0].State != JobSucceeded {
		t.Fatalf("unexpected persisted job: %#v", persisted[0])
	}
}

func TestJobManagerEnforcesGlobalLimitAndStartsQueuedFIFO(t *testing.T) {
	started := make(chan string, 4)
	manager := NewJobManager(1, func(ctx context.Context, job *AgentJob) {
		started <- job.ID
	})
	manager.SetLimits(2, 2, 4, 100)

	first := &AgentJob{ID: "first", ChatID: 1, Message: "one"}
	second := &AgentJob{ID: "second", ChatID: 2, Message: "two"}
	third := &AgentJob{ID: "third", ChatID: 3, Message: "three"}
	for _, job := range []*AgentJob{first, second, third} {
		if _, err := manager.Enqueue(context.Background(), job); err != nil {
			t.Fatalf("Enqueue(%s) returned error: %v", job.ID, err)
		}
	}

	got := map[string]bool{<-started: true, <-started: true}
	if !got["first"] || !got["second"] {
		t.Fatalf("unexpected initial global starts: %#v", got)
	}
	select {
	case id := <-started:
		t.Fatalf("global limit should queue third job, started %s", id)
	case <-time.After(50 * time.Millisecond):
	}

	manager.Finish(first, JobSucceeded, "default", "")
	if id := <-started; id != "third" {
		t.Fatalf("expected FIFO queued job third, got %s", id)
	}
	_, _ = manager.Cancel(2, "second")
	_, _ = manager.Cancel(3, "third")
}

func TestJobManagerEnforcesQueueAndRateLimits(t *testing.T) {
	block := make(chan struct{})
	manager := NewJobManager(1, func(ctx context.Context, job *AgentJob) {
		<-block
	})
	manager.SetLimits(1, 1, 2, 2)

	if _, err := manager.Enqueue(context.Background(), &AgentJob{ID: "active", ChatID: 1}); err != nil {
		t.Fatalf("active enqueue failed: %v", err)
	}
	if _, err := manager.Enqueue(context.Background(), &AgentJob{ID: "queued", ChatID: 1}); err != nil {
		t.Fatalf("queued enqueue failed: %v", err)
	}
	if _, err := manager.Enqueue(context.Background(), &AgentJob{ID: "overflow", ChatID: 1}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected rate limit before third accepted request, got %v", err)
	}

	manager.SetLimits(1, 1, 2, 100)
	if _, err := manager.Enqueue(context.Background(), &AgentJob{ID: "queue-overflow", ChatID: 1}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected per-chat queue limit, got %v", err)
	}
	close(block)
}

func TestJobManagerConcurrentEnqueueRespectsGlobalLimit(t *testing.T) {
	var mu sync.Mutex
	running := 0
	peak := 0
	release := make(chan struct{})
	done := make(chan struct{}, 10)
	var manager *JobManager
	manager = NewJobManager(1, func(ctx context.Context, job *AgentJob) {
		mu.Lock()
		running++
		if running > peak {
			peak = running
		}
		mu.Unlock()
		<-release
		mu.Lock()
		running--
		mu.Unlock()
		manager.Finish(job, JobSucceeded, "default", "")
		done <- struct{}{}
	})
	manager.SetLimits(3, 5, 20, 100)

	for chatID := int64(1); chatID <= 10; chatID++ {
		if _, err := manager.Enqueue(context.Background(), &AgentJob{ChatID: chatID}); err != nil {
			t.Fatalf("enqueue chat %d: %v", chatID, err)
		}
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	gotPeak := peak
	mu.Unlock()
	if gotPeak != 3 {
		t.Fatalf("peak running jobs = %d, want 3", gotPeak)
	}
	close(release)
	for i := 0; i < 10; i++ {
		<-done
	}
}
