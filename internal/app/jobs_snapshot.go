package app

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

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
