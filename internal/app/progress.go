package app

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"
)

func (a *App) sendMessage(ctx context.Context, chatID int64, text string, replyTo int64) error {
	return a.sendMessageWithMarkup(ctx, chatID, text, replyTo, nil)
}

func (a *App) sendMessageWithMarkup(ctx context.Context, chatID int64, text string, replyTo int64, markup *InlineKeyboardMarkup) error {
	_, err := a.sendMessageWithMarkupResult(ctx, chatID, text, replyTo, markup)
	return err
}

func (a *App) sendMessageWithMarkupResult(ctx context.Context, chatID int64, text string, replyTo int64, markup *InlineKeyboardMarkup) ([]TelegramMessage, error) {
	if a.bot == nil {
		return nil, nil
	}
	options := SendMessageOptions{
		ReplyMarkup: markup,
	}
	return a.bot.SendMessageWithOptions(ctx, chatID, text, replyTo, options)
}

func (a *App) editMessageWithMarkup(ctx context.Context, chatID, messageID int64, text string, markup *InlineKeyboardMarkup) error {
	if a.bot == nil || messageID <= 0 {
		return nil
	}
	return a.bot.EditMessageText(ctx, chatID, messageID, text, SendMessageOptions{
		ReplyMarkup: markup,
	})
}

func (a *App) updateJobProgress(ctx context.Context, job *AgentJob, progressID int64, text string, markup *InlineKeyboardMarkup) {
	if progressID <= 0 {
		return
	}
	if err := a.editMessageWithMarkup(ctx, job.ChatID, progressID, text, markup); err != nil {
		log.Printf("edit job progress failed chat_id=%d job=%s message_id=%d: %s", job.ChatID, job.ID, progressID, safeError(err))
	}
}

func formatJobProgress(job *AgentJob, status, detail string) string {
	var out strings.Builder
	out.WriteString(strings.TrimSpace(status))
	if out.Len() == 0 {
		out.WriteString("작업 상태")
	}
	out.WriteString("\njob: ")
	out.WriteString(job.ID)
	if job.AgentName != "" {
		out.WriteString("\nagent: ")
		out.WriteString(job.AgentName)
	}
	if detail = strings.TrimSpace(detail); detail != "" {
		out.WriteString("\n")
		out.WriteString(detail)
	}
	return out.String()
}

func (a *App) keepTyping(ctx context.Context, chatID int64, done <-chan struct{}) {
	if a.bot == nil {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	_ = a.bot.SendChatAction(ctx, chatID, "typing")
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			_ = a.bot.SendChatAction(ctx, chatID, "typing")
		}
	}
}

func (a *App) keepJobProgress(ctx context.Context, job *AgentJob, progressID int64, done <-chan struct{}) {
	timer := time.NewTimer(jobProgressInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-timer.C:
			elapsed := time.Since(job.StartedAt).Round(time.Second)
			if elapsed < 0 {
				elapsed = 0
			}
			a.updateJobProgress(ctx, job, progressID, formatJobProgress(job, "작업 진행 중입니다.", "elapsed: "+elapsed.String()), jobRunningActions(job.ID))
			timer.Reset(jobProgressInterval)
		}
	}
}

// askWithLiveProgress runs a streaming agent and edits the job progress message
// with a throttled, redacted preview of the answer as it is produced.
func (a *App) askWithLiveProgress(ctx context.Context, job *AgentJob, progressID int64, agent StreamingAgent, prompt string) (string, error) {
	var (
		mu    sync.Mutex
		buf   strings.Builder
		dirty bool
	)
	streamDone := make(chan struct{})
	pumpDone := make(chan struct{})

	go func() {
		defer close(pumpDone)
		ticker := time.NewTicker(streamEditInterval)
		defer ticker.Stop()
		for {
			select {
			case <-streamDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				mu.Lock()
				if !dirty {
					mu.Unlock()
					continue
				}
				preview := redactSecrets(streamPreview(buf.String()))
				dirty = false
				mu.Unlock()
				if preview == "" {
					continue
				}
				a.updateJobProgress(ctx, job, progressID, formatJobProgress(job, "답변 생성 중입니다.", preview), jobRunningActions(job.ID))
			}
		}
	}()

	answer, err := agent.AskStream(ctx, prompt, func(delta string) {
		mu.Lock()
		buf.WriteString(delta)
		dirty = true
		mu.Unlock()
	})
	close(streamDone)
	<-pumpDone
	return answer, err
}

func streamPreview(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) > streamPreviewMaxRunes {
		text = "…" + string(runes[len(runes)-streamPreviewMaxRunes:])
	}
	return text
}

func (a *App) finishJobError(ctx context.Context, job *AgentJob, agentName, prefix string, err error) {
	state := JobFailed
	errText := redactSecrets(err.Error())
	message := prefix + "\n\n" + errText
	sendCtx := ctx
	if errors.Is(err, context.Canceled) || ctx.Err() != nil {
		state = JobCanceled
		message = "작업이 취소되었습니다.\njob: " + job.ID
		sendCtx = context.Background()
	}
	progressID := a.jobs.ProgressMessageID(job)
	if state == JobCanceled {
		a.updateJobProgress(sendCtx, job, progressID, formatJobProgress(job, "작업이 취소되었습니다.", ""), jobCanceledActions(job.ID))
	} else {
		a.updateJobProgress(sendCtx, job, progressID, formatJobProgress(job, "작업이 실패했습니다.", truncate(errText, 240)), jobFailedActions(job.ID))
	}
	_ = a.sendMessage(sendCtx, job.ChatID, message, job.ReplyTo)
	a.jobs.Finish(job, state, agentName, errText)
}
