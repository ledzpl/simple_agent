package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type JobState string

const (
	JobQueued    JobState = "queued"
	JobRunning   JobState = "running"
	JobSucceeded JobState = "succeeded"
	JobFailed    JobState = "failed"
	JobCanceled  JobState = "canceled"
)

type AgentJob struct {
	ID          string
	ChatID      int64
	UserID      int64
	ReplyTo     int64
	Message     string
	ForceDebate bool
	State       JobState
	AgentName   string
	Error       string
	CreatedAt   time.Time
	StartedAt   time.Time
	FinishedAt  time.Time

	parentCtx context.Context
	cancel    context.CancelFunc
}

type JobSnapshot struct {
	ID          string
	ChatID      int64
	UserID      int64
	ReplyTo     int64
	Message     string
	ForceDebate bool
	State       JobState
	AgentName   string
	Error       string
	CreatedAt   time.Time
	StartedAt   time.Time
	FinishedAt  time.Time
}

type JobManager struct {
	mu           sync.Mutex
	maxActive    int
	run          func(context.Context, *AgentJob)
	active       map[int64][]*AgentJob
	queued       map[int64][]*AgentJob
	history      map[int64][]*AgentJob
	historyLimit int
}

func NewJobManager(maxActive int, run func(context.Context, *AgentJob)) *JobManager {
	if maxActive <= 0 {
		maxActive = 1
	}
	return &JobManager{
		maxActive:    maxActive,
		run:          run,
		active:       map[int64][]*AgentJob{},
		queued:       map[int64][]*AgentJob{},
		history:      map[int64][]*AgentJob{},
		historyLimit: 20,
	}
}

func (m *JobManager) Enqueue(ctx context.Context, job *AgentJob) JobSnapshot {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if job.ID == "" {
		job.ID = newJobID()
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}
	job.parentCtx = ctx
	job.State = JobQueued
	if len(m.active[job.ChatID]) >= m.maxActive {
		m.queued[job.ChatID] = append(m.queued[job.ChatID], job)
		return snapshotJob(job)
	}
	m.startLocked(ctx, job)
	return snapshotJob(job)
}

func (m *JobManager) Status(chatID int64) []JobSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []JobSnapshot
	for _, job := range m.active[chatID] {
		out = append(out, snapshotJob(job))
	}
	for _, job := range m.queued[chatID] {
		out = append(out, snapshotJob(job))
	}
	for i := len(m.history[chatID]) - 1; i >= 0 && len(out) < 10; i-- {
		out = append(out, snapshotJob(m.history[chatID][i]))
	}
	return out
}

func (m *JobManager) Cancel(chatID int64, selector string) ([]JobSnapshot, error) {
	selector = strings.TrimSpace(strings.ToLower(selector))
	if selector == "" {
		selector = "latest"
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	var canceled []JobSnapshot
	cancelOne := func(job *AgentJob) {
		job.State = JobCanceled
		job.FinishedAt = time.Now().UTC()
		job.Error = "canceled by user"
		if job.cancel != nil {
			job.cancel()
		}
		canceled = append(canceled, snapshotJob(job))
	}

	if selector == "all" {
		for _, job := range m.active[chatID] {
			cancelOne(job)
		}
		for _, job := range m.queued[chatID] {
			cancelOne(job)
			m.addHistoryLocked(job)
		}
		m.queued[chatID] = nil
		return canceled, nil
	}

	if selector == "latest" {
		if queued := m.queued[chatID]; len(queued) > 0 {
			job := queued[len(queued)-1]
			m.queued[chatID] = queued[:len(queued)-1]
			cancelOne(job)
			m.addHistoryLocked(job)
			return canceled, nil
		}
		if active := m.active[chatID]; len(active) > 0 {
			cancelOne(active[len(active)-1])
			return canceled, nil
		}
		return nil, fmt.Errorf("no active or queued jobs")
	}

	for i, job := range m.queued[chatID] {
		if strings.EqualFold(job.ID, selector) {
			m.queued[chatID] = append(m.queued[chatID][:i], m.queued[chatID][i+1:]...)
			cancelOne(job)
			m.addHistoryLocked(job)
			return canceled, nil
		}
	}
	for _, job := range m.active[chatID] {
		if strings.EqualFold(job.ID, selector) {
			cancelOne(job)
			return canceled, nil
		}
	}
	return nil, fmt.Errorf("job %q was not found", selector)
}

func (m *JobManager) Retry(ctx context.Context, chatID int64, selector string) (JobSnapshot, error) {
	source, err := m.RetrySource(chatID, selector)
	if err != nil {
		return JobSnapshot{}, err
	}
	return m.Enqueue(ctx, &AgentJob{
		ChatID:      source.ChatID,
		UserID:      source.UserID,
		ReplyTo:     source.ReplyTo,
		Message:     source.Message,
		ForceDebate: source.ForceDebate,
	}), nil
}

func (m *JobManager) RetrySource(chatID int64, selector string) (JobSnapshot, error) {
	selector = strings.TrimSpace(strings.ToLower(selector))
	if selector == "" {
		selector = "last"
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	source := m.retrySourceLocked(chatID, selector)
	if source == nil {
		return JobSnapshot{}, fmt.Errorf("no retryable job found")
	}
	return snapshotJob(source), nil
}

func (m *JobManager) retrySourceLocked(chatID int64, selector string) *AgentJob {
	var source *AgentJob
	history := m.history[chatID]
	if selector == "last" || selector == "latest" {
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].Message != "" {
				source = history[i]
				break
			}
		}
	} else {
		for i := len(history) - 1; i >= 0; i-- {
			if strings.EqualFold(history[i].ID, selector) {
				source = history[i]
				break
			}
		}
	}
	return source
}

func (m *JobManager) SetAgentName(job *AgentJob, agentName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job.AgentName = agentName
}

func (m *JobManager) Finish(job *AgentJob, state JobState, agentName, errText string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	job.State = state
	job.AgentName = agentName
	job.Error = errText
	job.FinishedAt = time.Now().UTC()
	m.removeActiveLocked(job)
	m.addHistoryLocked(job)

	for {
		queued := m.queued[job.ChatID]
		if len(queued) == 0 {
			return
		}
		next := queued[0]
		m.queued[job.ChatID] = queued[1:]
		if next.parentCtx == nil {
			next.parentCtx = context.Background()
		}
		if err := next.parentCtx.Err(); err != nil {
			next.State = JobCanceled
			next.Error = err.Error()
			next.FinishedAt = time.Now().UTC()
			m.addHistoryLocked(next)
			continue
		}
		m.startLocked(next.parentCtx, next)
		return
	}
}

func (m *JobManager) startLocked(ctx context.Context, job *AgentJob) {
	jobCtx, cancel := context.WithCancel(ctx)
	job.cancel = cancel
	job.State = JobRunning
	job.StartedAt = time.Now().UTC()
	m.active[job.ChatID] = append(m.active[job.ChatID], job)
	go m.run(jobCtx, job)
}

func (m *JobManager) removeActiveLocked(job *AgentJob) {
	active := m.active[job.ChatID]
	for i, candidate := range active {
		if candidate == job {
			m.active[job.ChatID] = append(active[:i], active[i+1:]...)
			return
		}
	}
}

func (m *JobManager) addHistoryLocked(job *AgentJob) {
	history := append(m.history[job.ChatID], job)
	if len(history) > m.historyLimit {
		history = history[len(history)-m.historyLimit:]
	}
	m.history[job.ChatID] = history
}

func snapshotJob(job *AgentJob) JobSnapshot {
	return JobSnapshot{
		ID:          job.ID,
		ChatID:      job.ChatID,
		UserID:      job.UserID,
		ReplyTo:     job.ReplyTo,
		Message:     job.Message,
		ForceDebate: job.ForceDebate,
		State:       job.State,
		AgentName:   job.AgentName,
		Error:       job.Error,
		CreatedAt:   job.CreatedAt,
		StartedAt:   job.StartedAt,
		FinishedAt:  job.FinishedAt,
	}
}

func newJobID() string {
	return "j" + strings.ToLower(strconv.FormatInt(time.Now().UnixNano(), 36))
}

func formatJobStatus(jobs []JobSnapshot) string {
	if len(jobs) == 0 {
		return "작업이 없습니다."
	}
	sort.SliceStable(jobs, func(i, j int) bool {
		return jobs[i].CreatedAt.After(jobs[j].CreatedAt)
	})
	var out strings.Builder
	out.WriteString("작업 상태:")
	for _, job := range jobs {
		out.WriteString("\n")
		out.WriteString(formatJobLine(job))
	}
	return out.String()
}

func formatJobLine(job JobSnapshot) string {
	age := time.Since(job.CreatedAt).Round(time.Second)
	if age < 0 {
		age = 0
	}
	text := fmt.Sprintf("- %s [%s] %s", job.ID, job.State, truncate(redactSecrets(strings.Join(strings.Fields(job.Message), " ")), 80))
	if job.AgentName != "" {
		text += " agent=" + job.AgentName
	}
	if job.Error != "" {
		text += " error=" + truncate(redactSecrets(job.Error), 80)
	}
	text += fmt.Sprintf(" age=%s", age)
	return text
}
