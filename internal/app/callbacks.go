package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (a *App) handleCallback(ctx context.Context, query TelegramCallbackQuery) {
	if query.Message == nil {
		a.answerCallback(ctx, query.ID, "처리할 메시지를 찾지 못했습니다.")
		return
	}
	chatID := query.Message.Chat.ID
	if err := a.authorizeCallback(query); err != nil {
		a.answerCallback(ctx, query.ID, err.Error())
		return
	}

	switch {
	case strings.HasPrefix(query.Data, "memory_delete:"):
		if a.memory == nil {
			a.answerCallback(ctx, query.ID, "대화 기억이 비활성화되어 있습니다.")
			return
		}
		memoryID := strings.TrimPrefix(query.Data, "memory_delete:")
		if err := a.memory.DeleteByID(chatID, memoryID); err != nil {
			a.answerCallback(ctx, query.ID, "삭제 실패: "+truncate(err.Error(), 160))
			return
		}
		a.answerCallback(ctx, query.ID, "이 답변의 저장 기억을 삭제했습니다.")
	case query.Data == "regenerate":
		if query.Message.ReplyToMessage == nil || strings.TrimSpace(query.Message.ReplyToMessage.Text) == "" {
			a.answerCallback(ctx, query.ID, "다시 생성할 원문 메시지를 찾지 못했습니다.")
			return
		}
		a.answerCallback(ctx, query.ID, "다시 생성합니다.")
		a.handleMessage(ctx, *query.Message.ReplyToMessage)
	case strings.HasPrefix(query.Data, "debate_show:"):
		id := strings.TrimPrefix(query.Data, "debate_show:")
		transcript := a.loadDebateTranscript(id)
		if strings.TrimSpace(transcript) == "" {
			a.answerCallback(ctx, query.ID, "토론 기록이 만료되었습니다.")
			return
		}
		a.answerCallback(ctx, query.ID, "토론 기록을 보냅니다.")
		_ = a.sendMessageWithMarkup(ctx, chatID, transcript, query.Message.MessageID, hideMessageActions())
	case strings.HasPrefix(query.Data, "job_cancel:"):
		jobID := strings.TrimPrefix(query.Data, "job_cancel:")
		canceled, err := a.jobs.Cancel(chatID, jobID)
		if err != nil {
			a.answerCallback(ctx, query.ID, "취소 실패: "+truncate(err.Error(), 120))
			return
		}
		a.answerCallback(ctx, query.ID, "작업을 취소했습니다.")
		_ = a.editMessageWithMarkup(ctx, chatID, query.Message.MessageID, formatJobStatus(canceled), jobCanceledActions(jobID))
	case strings.HasPrefix(query.Data, "job_retry:"):
		jobID := strings.TrimPrefix(query.Data, "job_retry:")
		source, err := a.jobs.RetrySource(chatID, jobID)
		if err != nil {
			a.answerCallback(ctx, query.ID, "재시도 실패: "+truncate(err.Error(), 120))
			return
		}
		if a.needsDangerConfirmation(source.Message) {
			confirmationMessage := *query.Message
			confirmationMessage.From = query.From
			confirmationMessage.Text = source.Message
			confirmation := a.confirmations.Add(confirmationMessage, source.Message, source.ForceDebate)
			a.answerCallback(ctx, query.ID, "확인이 필요합니다.")
			_ = a.sendMessage(ctx, chatID, fmt.Sprintf("이전 작업 요청이 위험할 수 있습니다. 다시 실행하려면 다음 명령을 보내세요.\n/confirm %s", confirmation.ID), query.Message.MessageID)
			return
		}
		snapshot, err := a.jobs.Enqueue(ctx, &AgentJob{
			ChatID:      source.ChatID,
			UserID:      source.UserID,
			ReplyTo:     source.ReplyTo,
			Message:     source.Message,
			ForceDebate: source.ForceDebate,
		})
		if err != nil {
			a.answerCallback(ctx, query.ID, jobEnqueueError(err))
			return
		}
		a.answerCallback(ctx, query.ID, "작업을 다시 등록했습니다.")
		if snapshot.State == JobQueued {
			_ = a.sendMessageWithMarkup(ctx, chatID, fmt.Sprintf("작업을 다시 등록했습니다.\n%s", formatJobLine(snapshot)), query.Message.MessageID, jobQueuedActions(snapshot.ID))
		}
	case query.Data == "delete_message":
		a.answerCallback(ctx, query.ID, "숨겼습니다.")
		if a.bot != nil {
			_ = a.bot.DeleteMessage(ctx, chatID, query.Message.MessageID)
		}
	default:
		a.answerCallback(ctx, query.ID, "알 수 없는 액션입니다.")
	}
}

func (a *App) answerCallback(ctx context.Context, callbackID, text string) {
	if a.bot == nil || callbackID == "" {
		return
	}
	_ = a.bot.AnswerCallbackQuery(ctx, callbackID, text)
}

func (a *App) answerActions(memoryID string, transcript []DebateTurn) *InlineKeyboardMarkup {
	row := []InlineKeyboardButton{
		{Text: "다시 생성", CallbackData: "regenerate"},
	}
	if a.memory != nil && strings.TrimSpace(memoryID) != "" {
		row = append(row, InlineKeyboardButton{Text: "기억 삭제", CallbackData: "memory_delete:" + memoryID})
	}
	var keyboard [][]InlineKeyboardButton
	keyboard = append(keyboard, row)
	if len(transcript) > 0 {
		id := a.storeDebateTranscript(redactSecrets(formatDebateTranscript(transcript)))
		keyboard = append(keyboard, []InlineKeyboardButton{{Text: "토론 보기", CallbackData: "debate_show:" + id}})
	}
	return &InlineKeyboardMarkup{InlineKeyboard: keyboard}
}

func jobQueuedActions(jobID string) *InlineKeyboardMarkup {
	if strings.TrimSpace(jobID) == "" {
		return nil
	}
	return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{
		{Text: "취소", CallbackData: "job_cancel:" + jobID},
	}}}
}

func jobRunningActions(jobID string) *InlineKeyboardMarkup {
	return jobQueuedActions(jobID)
}

func jobDoneActions(jobID string) *InlineKeyboardMarkup {
	if strings.TrimSpace(jobID) == "" {
		return nil
	}
	return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{
		{Text: "재시도", CallbackData: "job_retry:" + jobID},
	}}}
}

func jobFailedActions(jobID string) *InlineKeyboardMarkup {
	return jobDoneActions(jobID)
}

func jobCanceledActions(jobID string) *InlineKeyboardMarkup {
	return jobDoneActions(jobID)
}

func hideMessageActions() *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{
		{Text: "토론 숨기기", CallbackData: "delete_message"},
	}}}
}

func (a *App) storeDebateTranscript(transcript string) string {
	a.debateMu.Lock()
	defer a.debateMu.Unlock()
	if len(a.debateTranscripts) > 100 {
		a.debateTranscripts = map[string]string{}
	}
	id := strconv.FormatInt(time.Now().UnixNano(), 36)
	a.debateTranscripts[id] = transcript
	return id
}

func (a *App) loadDebateTranscript(id string) string {
	a.debateMu.RLock()
	defer a.debateMu.RUnlock()
	return a.debateTranscripts[id]
}
