package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

type App struct {
	cfg    Config
	bot    *TelegramBot
	router *AgentRouter
	memory *MemoryStore
	state  *StateStore

	jobs              *JobManager
	confirmations     *ConfirmationStore
	debateTranscripts map[string]string
}

func NewApp(cfg Config, bot *TelegramBot, agent Agent, memory *MemoryStore) *App {
	return NewAppWithRouter(cfg, bot, NewSingleAgentRouter(defaultAgentName, "Default local agent", []string{"*"}, agent, cfg.AgentBackend), memory)
}

func NewAppWithRouter(cfg Config, bot *TelegramBot, router *AgentRouter, memory *MemoryStore) *App {
	app := &App{
		cfg:               cfg,
		bot:               bot,
		router:            router,
		memory:            memory,
		confirmations:     NewConfirmationStore(10 * time.Minute),
		debateTranscripts: map[string]string{},
	}
	app.jobs = NewJobManager(maxActiveJobsPerChat, app.executeJob)
	app.jobs.SetHistoryLimit(jobHistoryLimit)
	return app
}

func (a *App) UseStateStore(ctx context.Context, state *StateStore) error {
	a.state = state
	if state == nil || a.jobs == nil {
		return nil
	}
	history, err := state.LoadJobHistory(ctx)
	if err != nil {
		return err
	}
	a.jobs.LoadHistory(history)
	a.jobs.SetHistorySink(func(jobs []JobSnapshot) {
		if err := state.SaveJobHistory(jobs); err != nil {
			log.Printf("save job history failed: %v", err)
		}
	})
	return nil
}

func (a *App) Run(ctx context.Context) error {
	var offset int64
	backoff := time.Second
	if a.state != nil {
		loaded, err := a.state.LoadOffset(ctx)
		if err != nil {
			return err
		}
		offset = loaded
		if offset > 0 {
			log.Printf("loaded telegram update offset=%d", offset)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		updates, err := a.bot.GetUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("getUpdates failed: %v", err)
			sleep(ctx, backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second

		for _, update := range updates {
			nextOffset := offset
			if update.UpdateID >= offset {
				nextOffset = update.UpdateID + 1
			}
			if update.Message != nil {
				a.handleMessage(ctx, *update.Message)
			}
			if update.CallbackQuery != nil {
				a.handleCallback(ctx, *update.CallbackQuery)
			}
			if nextOffset > offset {
				if err := a.saveOffset(ctx, nextOffset); err != nil {
					return err
				}
				offset = nextOffset
			}
		}
	}
}

func (a *App) saveOffset(ctx context.Context, offset int64) error {
	if a.state == nil {
		return nil
	}
	if err := a.state.SaveOffset(ctx, offset); err != nil {
		return fmt.Errorf("save telegram update offset %d: %w", offset, err)
	}
	return nil
}

func (a *App) handleMessage(ctx context.Context, msg TelegramMessage) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		_ = a.sendMessage(ctx, msg.Chat.ID, "텍스트 메시지만 처리할 수 있습니다.", msg.MessageID)
		return
	}

	command := normalizeCommand(text)
	switch command {
	case "/start", "/id":
		_ = a.sendMessage(ctx, msg.Chat.ID, a.identityMessage(msg), msg.MessageID)
		return
	case "/help":
		_ = a.sendMessage(ctx, msg.Chat.ID, helpMessage(), msg.MessageID)
		return
	case "/agents":
		a.handleAgents(ctx, msg)
		return
	case "/status":
		a.handleStatus(ctx, msg)
		return
	case "/cancel":
		a.handleCancel(ctx, msg)
		return
	case "/retry":
		a.handleRetry(ctx, msg)
		return
	case "/confirm":
		a.handleConfirm(ctx, msg)
		return
	case "/route":
		a.handleRoute(ctx, msg)
		return
	case "/debate":
		// Parsed after allowlist validation below so only approved chats can run agents.
	case "/memory":
		a.handleMemory(ctx, msg)
		return
	case "/reset":
		a.handleReset(ctx, msg)
		return
	}

	if err := a.authorizeMessage(msg); err != nil {
		_ = a.sendMessage(ctx, msg.Chat.ID, err.Error(), msg.MessageID)
		log.Printf("denied chat_id=%d text=%q reason=%s", msg.Chat.ID, truncate(redactSecrets(text), 80), redactSecrets(err.Error()))
		return
	}

	forceDebate := false
	if normalizeCommand(text) == "/debate" {
		message, ok := parseMessageCommand(text, "/debate")
		if !ok {
			_ = a.sendMessage(ctx, msg.Chat.ID, "사용법: /debate <message>", msg.MessageID)
			return
		}
		text = message
		forceDebate = true
	}

	if a.needsDangerConfirmation(text) {
		confirmation := a.confirmations.Add(msg, text, forceDebate)
		_ = a.sendMessage(ctx, msg.Chat.ID, fmt.Sprintf("위험할 수 있는 작업 요청입니다. 실행하려면 다음 명령을 보내세요.\n/confirm %s", confirmation.ID), msg.MessageID)
		return
	}

	a.enqueueAgentJob(ctx, msg, text, forceDebate)
}

func (a *App) enqueueAgentJob(ctx context.Context, msg TelegramMessage, text string, forceDebate bool) JobSnapshot {
	userID := int64(0)
	if msg.From != nil {
		userID = msg.From.ID
	}
	snapshot := a.jobs.Enqueue(ctx, &AgentJob{
		ChatID:      msg.Chat.ID,
		UserID:      userID,
		ReplyTo:     msg.MessageID,
		Message:     text,
		ForceDebate: forceDebate,
	})
	if snapshot.State == JobQueued {
		_ = a.sendMessageWithMarkup(ctx, msg.Chat.ID, fmt.Sprintf("작업이 대기열에 추가되었습니다.\njob: %s\n/status 로 상태를 볼 수 있고 /cancel %s 로 취소할 수 있습니다.", snapshot.ID, snapshot.ID), msg.MessageID, jobQueuedActions(snapshot.ID))
	}
	return snapshot
}

func (a *App) executeJob(ctx context.Context, job *AgentJob) {
	progressID := int64(0)
	if sent, err := a.sendMessageWithMarkupResult(ctx, job.ChatID, formatJobProgress(job, "작업을 시작합니다.", ""), job.ReplyTo, jobRunningActions(job.ID)); err == nil && len(sent) > 0 {
		progressID = sent[len(sent)-1].MessageID
		a.jobs.SetProgressMessageID(job, progressID)
	} else if err != nil {
		log.Printf("send job progress failed chat_id=%d job=%s: %v", job.ChatID, job.ID, err)
	}

	route, err := a.routeMessage(job.Message)
	if err != nil {
		a.finishJobError(ctx, job, "", "agent 라우팅에 실패했습니다.", err)
		return
	}
	a.jobs.SetAgentName(job, route.Runner.Name)

	log.Printf("handling job=%s chat_id=%d agent=%s backend=%s route=%s", job.ID, job.ChatID, route.Runner.Name, route.Runner.Backend, route.Reason)

	memoryContext, err := a.memoryContext(ctx, job.ChatID)
	if err != nil {
		log.Printf("memory load failed chat_id=%d: %v", job.ChatID, err)
		a.finishJobError(ctx, job, route.Runner.Name, "저장된 대화 기억을 읽지 못했습니다.", err)
		return
	}
	prompt := promptWithMemory(memoryContext, route.Message)

	done := make(chan struct{})
	go a.keepTyping(ctx, job.ChatID, done)
	go a.keepJobProgress(ctx, job, progressID, done)

	answerAgent := route.Runner
	answer := ""
	var transcript []DebateTurn
	if a.shouldDebate(route, job.ForceDebate) {
		result, err := a.answerWithDebate(ctx, job.ChatID, route.Message, memoryContext)
		close(done)
		if err != nil {
			log.Printf("debate failed job=%s chat_id=%d: %v", job.ID, job.ChatID, err)
			a.finishJobError(ctx, job, route.Runner.Name, "agent 토론 실행에 실패했습니다.", err)
			return
		}
		answer = result.Final
		answerAgent = result.Synthesizer
		transcript = result.Transcript
	} else {
		var err error
		answer, err = route.Runner.Agent.Ask(ctx, prompt)
		close(done)
		if err != nil {
			log.Printf("agent failed job=%s chat_id=%d agent=%s: %v", job.ID, job.ChatID, route.Runner.Name, err)
			a.finishJobError(ctx, job, route.Runner.Name, "로컬 에이전트 실행에 실패했습니다.", err)
			return
		}
	}
	if ctx.Err() != nil {
		a.finishJobError(ctx, job, answerAgent.Name, "작업이 취소되었습니다.", ctx.Err())
		return
	}

	if strings.TrimSpace(answer) == "" {
		a.finishJobError(ctx, job, answerAgent.Name, "agent가 빈 응답을 반환했습니다.", errors.New("empty answer"))
		return
	}

	memoryID := ""
	if a.memory != nil {
		memoryID = newMemoryID()
	}
	safeAnswer := redactSecrets(answer)
	if err := a.sendMessageWithMarkup(ctx, job.ChatID, safeAnswer, job.ReplyTo, a.answerActions(memoryID, transcript)); err != nil {
		log.Printf("send answer failed chat_id=%d: %v", job.ChatID, err)
		a.finishJobError(ctx, job, answerAgent.Name, "Telegram 답변 전송에 실패했습니다.", err)
		return
	}
	a.updateJobProgress(context.Background(), job, progressID, formatJobProgress(job, "답변을 전송했습니다.", "기억을 정리 중입니다."), nil)

	if _, err := a.rememberWithAgentID(ctx, answerAgent.Agent, job.ChatID, route.Message, safeAnswer, memoryID); err != nil {
		log.Printf("memory append failed chat_id=%d: %v", job.ChatID, err)
	}
	a.jobs.Finish(job, JobSucceeded, answerAgent.Name, "")
	a.updateJobProgress(context.Background(), job, progressID, formatJobProgress(job, "작업이 완료되었습니다.", ""), jobDoneActions(job.ID))
}

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
		transcript := a.debateTranscripts[id]
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
		snapshot := a.jobs.Enqueue(ctx, &AgentJob{
			ChatID:      source.ChatID,
			UserID:      source.UserID,
			ReplyTo:     source.ReplyTo,
			Message:     source.Message,
			ForceDebate: source.ForceDebate,
		})
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
		id := a.storeDebateTranscript(formatDebateTranscript(transcript))
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

func (a *App) updateJobProgress(ctx context.Context, job *AgentJob, progressID int64, text string, markup *InlineKeyboardMarkup) {
	if progressID <= 0 {
		return
	}
	if err := a.editMessageWithMarkup(ctx, job.ChatID, progressID, text, markup); err != nil {
		log.Printf("edit job progress failed chat_id=%d job=%s message_id=%d: %v", job.ChatID, job.ID, progressID, err)
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

func (a *App) storeDebateTranscript(transcript string) string {
	if len(a.debateTranscripts) > 100 {
		a.debateTranscripts = map[string]string{}
	}
	id := strconv.FormatInt(time.Now().UnixNano(), 36)
	a.debateTranscripts[id] = transcript
	return id
}

func (a *App) shouldDebate(route AgentRoute, forceDebate bool) bool {
	if forceDebate {
		return true
	}
	if route.Forced {
		return false
	}
	return a.cfg.DebateEnabled
}

func (a *App) routeMessage(text string) (AgentRoute, error) {
	if normalizeCommand(text) == "/agent" {
		name, message, ok := parseAgentCommand(text)
		if !ok {
			return AgentRoute{}, fmt.Errorf("사용법: /agent <agent-name> <message>")
		}
		return a.router.RouteTo(name, message)
	}
	return a.router.Route(text), nil
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
	interval := jobProgressInterval
	timer := time.NewTimer(interval)
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
			timer.Reset(interval)
		}
	}
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

func (a *App) isAllowed(chatID int64) bool {
	return a.isChatAllowed(chatID)
}

func normalizeCommand(text string) string {
	first := strings.Fields(text)
	if len(first) == 0 {
		return ""
	}
	command := strings.ToLower(first[0])
	if at := strings.Index(command, "@"); at >= 0 {
		command = command[:at]
	}
	return command
}

func sleep(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "..."
}
