package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrQueueFull   = errors.New("job queue is full")
	ErrRateLimited = errors.New("job request rate limit exceeded")
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
	ID                string
	ChatID            int64
	UserID            int64
	ReplyTo           int64
	Message           string
	ForceDebate       bool
	State             JobState
	AgentName         string
	Error             string
	ProgressMessageID int64
	CreatedAt         time.Time
	StartedAt         time.Time
	FinishedAt        time.Time

	parentCtx context.Context
	cancel    context.CancelFunc
}

type JobSnapshot struct {
	ID                string    `json:"id"`
	ChatID            int64     `json:"chat_id"`
	UserID            int64     `json:"user_id"`
	ReplyTo           int64     `json:"reply_to"`
	Message           string    `json:"message"`
	ForceDebate       bool      `json:"force_debate"`
	State             JobState  `json:"state"`
	AgentName         string    `json:"agent_name,omitempty"`
	Error             string    `json:"error,omitempty"`
	ProgressMessageID int64     `json:"progress_message_id,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	StartedAt         time.Time `json:"started_at,omitempty"`
	FinishedAt        time.Time `json:"finished_at,omitempty"`
}

type JobManager struct {
	mu                   sync.Mutex
	maxActivePerChat     int
	maxActiveGlobal      int
	maxQueuedPerChat     int
	maxQueuedGlobal      int
	maxRequestsPerMinute int
	run                  func(context.Context, *AgentJob)
	active               map[int64][]*AgentJob
	activeCount          int
	queued               []*AgentJob
	recentRequests       map[int64][]time.Time
	history              map[int64][]*AgentJob
	historyLimit         int
	historySink          func([]JobSnapshot)
}

func NewJobManager(maxActivePerChat int, run func(context.Context, *AgentJob)) *JobManager {
	if maxActivePerChat <= 0 {
		maxActivePerChat = 1
	}
	return &JobManager{
		maxActivePerChat: maxActivePerChat,
		maxActiveGlobal:  maxActivePerChat,
		maxQueuedPerChat: 20,
		maxQueuedGlobal:  100,
		run:              run,
		active:           map[int64][]*AgentJob{},
		recentRequests:   map[int64][]time.Time{},
		history:          map[int64][]*AgentJob{},
		historyLimit:     20,
	}
}

func (m *JobManager) SetLimits(maxActiveGlobal, maxQueuedPerChat, maxQueuedGlobal, maxRequestsPerMinute int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if maxActiveGlobal <= 0 {
		maxActiveGlobal = 1
	}
	if maxQueuedPerChat <= 0 {
		maxQueuedPerChat = 1
	}
	if maxQueuedGlobal < maxQueuedPerChat {
		maxQueuedGlobal = maxQueuedPerChat
	}
	if maxRequestsPerMinute <= 0 {
		maxRequestsPerMinute = 1
	}
	m.maxActiveGlobal = maxActiveGlobal
	m.maxQueuedPerChat = maxQueuedPerChat
	m.maxQueuedGlobal = maxQueuedGlobal
	m.maxRequestsPerMinute = maxRequestsPerMinute
}

func (m *JobManager) SetHistoryLimit(limit int) {
	if limit <= 0 {
		limit = 20
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.historyLimit = limit
	for chatID, history := range m.history {
		if len(history) > m.historyLimit {
			m.history[chatID] = history[len(history)-m.historyLimit:]
		}
	}
}

func (m *JobManager) SetHistorySink(sink func([]JobSnapshot)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.historySink = sink
}

func (m *JobManager) LoadHistory(snapshots []JobSnapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.history = map[int64][]*AgentJob{}
	sort.SliceStable(snapshots, func(i, j int) bool {
		return snapshots[i].CreatedAt.Before(snapshots[j].CreatedAt)
	})
	for _, snapshot := range snapshots {
		if snapshot.ChatID == 0 || snapshot.ID == "" {
			continue
		}
		job := jobFromSnapshot(snapshot)
		m.history[job.ChatID] = append(m.history[job.ChatID], job)
		if len(m.history[job.ChatID]) > m.historyLimit {
			m.history[job.ChatID] = m.history[job.ChatID][len(m.history[job.ChatID])-m.historyLimit:]
		}
	}
}

func (m *JobManager) Enqueue(ctx context.Context, job *AgentJob) (JobSnapshot, error) {
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
	acceptedAt := time.Now().UTC()
	if err := m.checkRateLimitLocked(job.ChatID, acceptedAt); err != nil {
		return JobSnapshot{}, err
	}
	job.parentCtx = ctx
	job.State = JobQueued
	if !m.canStartLocked(job.ChatID) {
		if len(m.queued) >= m.maxQueuedGlobal {
			return JobSnapshot{}, fmt.Errorf("%w: global limit is %d", ErrQueueFull, m.maxQueuedGlobal)
		}
		if m.queuedCountForChatLocked(job.ChatID) >= m.maxQueuedPerChat {
			return JobSnapshot{}, fmt.Errorf("%w: chat limit is %d", ErrQueueFull, m.maxQueuedPerChat)
		}
		m.queued = append(m.queued, job)
		m.recordRequestLocked(job.ChatID, acceptedAt)
		return snapshotJob(job), nil
	}
	m.startLocked(ctx, job)
	m.recordRequestLocked(job.ChatID, acceptedAt)
	return snapshotJob(job), nil
}

func (m *JobManager) Status(chatID int64) []JobSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []JobSnapshot
	for _, job := range m.active[chatID] {
		out = append(out, snapshotJob(job))
	}
	for _, job := range m.queued {
		if job.ChatID == chatID {
			out = append(out, snapshotJob(job))
		}
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
		kept := m.queued[:0]
		for _, job := range m.queued {
			if job.ChatID == chatID {
				cancelOne(job)
				m.addHistoryLocked(job)
				continue
			}
			kept = append(kept, job)
		}
		m.queued = kept
		return canceled, nil
	}

	if selector == "latest" {
		for i := len(m.queued) - 1; i >= 0; i-- {
			if m.queued[i].ChatID == chatID {
				job := m.queued[i]
				m.queued = append(m.queued[:i], m.queued[i+1:]...)
				cancelOne(job)
				m.addHistoryLocked(job)
				return canceled, nil
			}
		}
		if active := m.active[chatID]; len(active) > 0 {
			cancelOne(active[len(active)-1])
			return canceled, nil
		}
		return nil, fmt.Errorf("no active or queued jobs")
	}

	for i, job := range m.queued {
		if job.ChatID == chatID && strings.EqualFold(job.ID, selector) {
			m.queued = append(m.queued[:i], m.queued[i+1:]...)
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
	})
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

func (m *JobManager) SetProgressMessageID(job *AgentJob, messageID int64) {
	if messageID <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job.ProgressMessageID = messageID
}

func (m *JobManager) ProgressMessageID(job *AgentJob) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return job.ProgressMessageID
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
	m.startAvailableLocked()
}

func (m *JobManager) startLocked(ctx context.Context, job *AgentJob) {
	jobCtx, cancel := context.WithCancel(ctx)
	job.cancel = cancel
	job.State = JobRunning
	job.StartedAt = time.Now().UTC()
	m.active[job.ChatID] = append(m.active[job.ChatID], job)
	m.activeCount++
	go m.run(jobCtx, job)
}

func (m *JobManager) removeActiveLocked(job *AgentJob) {
	active := m.active[job.ChatID]
	for i, candidate := range active {
		if candidate == job {
			m.active[job.ChatID] = append(active[:i], active[i+1:]...)
			if m.activeCount > 0 {
				m.activeCount--
			}
			return
		}
	}
}

func (m *JobManager) startAvailableLocked() {
	for m.activeCount < m.maxActiveGlobal {
		started := false
		for i := 0; i < len(m.queued); i++ {
			next := m.queued[i]
			if next.parentCtx == nil {
				next.parentCtx = context.Background()
			}
			if err := next.parentCtx.Err(); err != nil {
				m.queued = append(m.queued[:i], m.queued[i+1:]...)
				next.State = JobCanceled
				next.Error = err.Error()
				next.FinishedAt = time.Now().UTC()
				m.addHistoryLocked(next)
				i--
				continue
			}
			if len(m.active[next.ChatID]) >= m.maxActivePerChat {
				continue
			}
			m.queued = append(m.queued[:i], m.queued[i+1:]...)
			m.startLocked(next.parentCtx, next)
			started = true
			break
		}
		if !started {
			return
		}
	}
}

func (m *JobManager) canStartLocked(chatID int64) bool {
	return m.activeCount < m.maxActiveGlobal && len(m.active[chatID]) < m.maxActivePerChat
}

func (m *JobManager) queuedCountForChatLocked(chatID int64) int {
	count := 0
	for _, job := range m.queued {
		if job.ChatID == chatID {
			count++
		}
	}
	return count
}

func (m *JobManager) checkRateLimitLocked(chatID int64, now time.Time) error {
	if m.maxRequestsPerMinute <= 0 {
		return nil
	}
	cutoff := now.Add(-time.Minute)
	recent := m.recentRequests[chatID]
	firstValid := 0
	for firstValid < len(recent) && recent[firstValid].Before(cutoff) {
		firstValid++
	}
	recent = recent[firstValid:]
	m.recentRequests[chatID] = recent
	if len(recent) >= m.maxRequestsPerMinute {
		return fmt.Errorf("%w: maximum %d accepted jobs per minute per chat", ErrRateLimited, m.maxRequestsPerMinute)
	}
	return nil
}

func (m *JobManager) recordRequestLocked(chatID int64, now time.Time) {
	m.recentRequests[chatID] = append(m.recentRequests[chatID], now)
}

func (m *JobManager) addHistoryLocked(job *AgentJob) {
	history := append(m.history[job.ChatID], job)
	if len(history) > m.historyLimit {
		history = history[len(history)-m.historyLimit:]
	}
	m.history[job.ChatID] = history
	m.notifyHistorySinkLocked()
}

func snapshotJob(job *AgentJob) JobSnapshot {
	return JobSnapshot{
		ID:                job.ID,
		ChatID:            job.ChatID,
		UserID:            job.UserID,
		ReplyTo:           job.ReplyTo,
		Message:           job.Message,
		ForceDebate:       job.ForceDebate,
		State:             job.State,
		AgentName:         job.AgentName,
		Error:             job.Error,
		ProgressMessageID: job.ProgressMessageID,
		CreatedAt:         job.CreatedAt,
		StartedAt:         job.StartedAt,
		FinishedAt:        job.FinishedAt,
	}
}

func jobFromSnapshot(snapshot JobSnapshot) *AgentJob {
	return &AgentJob{
		ID:                snapshot.ID,
		ChatID:            snapshot.ChatID,
		UserID:            snapshot.UserID,
		ReplyTo:           snapshot.ReplyTo,
		Message:           snapshot.Message,
		ForceDebate:       snapshot.ForceDebate,
		State:             snapshot.State,
		AgentName:         snapshot.AgentName,
		Error:             snapshot.Error,
		ProgressMessageID: snapshot.ProgressMessageID,
		CreatedAt:         snapshot.CreatedAt,
		StartedAt:         snapshot.StartedAt,
		FinishedAt:        snapshot.FinishedAt,
	}
}

func (m *JobManager) notifyHistorySinkLocked() {
	if m.historySink == nil {
		return
	}
	m.historySink(m.historySnapshotsLocked())
}

func (m *JobManager) historySnapshotsLocked() []JobSnapshot {
	var out []JobSnapshot
	for _, history := range m.history {
		for _, job := range history {
			out = append(out, snapshotJob(job))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
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
