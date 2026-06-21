package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
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
	debateMu          sync.RWMutex
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
	app.jobs.SetLimits(maxActiveJobsGlobal, maxQueuedJobsPerChat, maxQueuedJobsGlobal, maxRequestsPerMinute)
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
			log.Printf("save job history failed: %s", safeError(err))
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
			log.Printf("getUpdates failed: %s", safeError(err))
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

	_, _ = a.enqueueAgentJob(ctx, msg, text, forceDebate)
}

func (a *App) enqueueAgentJob(ctx context.Context, msg TelegramMessage, text string, forceDebate bool) (JobSnapshot, error) {
	userID := int64(0)
	if msg.From != nil {
		userID = msg.From.ID
	}
	snapshot, err := a.jobs.Enqueue(ctx, &AgentJob{
		ChatID:      msg.Chat.ID,
		UserID:      userID,
		ReplyTo:     msg.MessageID,
		Message:     text,
		ForceDebate: forceDebate,
	})
	if err != nil {
		_ = a.sendMessage(ctx, msg.Chat.ID, jobEnqueueError(err), msg.MessageID)
		return JobSnapshot{}, err
	}
	if snapshot.State == JobQueued {
		_ = a.sendMessageWithMarkup(ctx, msg.Chat.ID, fmt.Sprintf("작업이 대기열에 추가되었습니다.\njob: %s\n/status 로 상태를 볼 수 있고 /cancel %s 로 취소할 수 있습니다.", snapshot.ID, snapshot.ID), msg.MessageID, jobQueuedActions(snapshot.ID))
	}
	return snapshot, nil
}

func (a *App) executeJob(ctx context.Context, job *AgentJob) {
	progressID := int64(0)
	if sent, err := a.sendMessageWithMarkupResult(ctx, job.ChatID, formatJobProgress(job, "작업을 시작합니다.", ""), job.ReplyTo, jobRunningActions(job.ID)); err == nil && len(sent) > 0 {
		progressID = sent[len(sent)-1].MessageID
		a.jobs.SetProgressMessageID(job, progressID)
	} else if err != nil {
		log.Printf("send job progress failed chat_id=%d job=%s: %s", job.ChatID, job.ID, safeError(err))
	}

	route, err := a.routeMessage(job.Message)
	if err != nil {
		a.finishJobError(ctx, job, "", "agent 라우팅에 실패했습니다.", err)
		return
	}
	a.jobs.SetAgentName(job, route.Runner.Name)

	log.Printf("handling job=%s chat_id=%d agent=%s backend=%s route=%s", job.ID, job.ChatID, route.Runner.Name, route.Runner.Backend, route.Reason)

	memoryContext, err := a.memoryContext(ctx, job.ChatID, route.Message)
	if err != nil {
		log.Printf("memory load failed chat_id=%d: %s", job.ChatID, safeError(err))
		a.finishJobError(ctx, job, route.Runner.Name, "저장된 대화 기억을 읽지 못했습니다.", err)
		return
	}
	prompt := promptWithMemory(memoryContext, route.Message)

	debate := a.shouldDebate(route, job.ForceDebate)
	streamer, canStream := route.Runner.Agent.(StreamingAgent)
	// When streaming, the stream pump owns the progress message, so the periodic
	// progress updater must not edit the same message concurrently.
	liveStream := canStream && !debate && a.bot != nil && progressID > 0

	activityCtx, cancelActivity := context.WithCancel(ctx)
	done := make(chan struct{})
	var activityWG sync.WaitGroup
	activityWG.Add(1)
	go func() {
		defer activityWG.Done()
		a.keepTyping(activityCtx, job.ChatID, done)
	}()
	if !liveStream {
		activityWG.Add(1)
		go func() {
			defer activityWG.Done()
			a.keepJobProgress(activityCtx, job, progressID, done)
		}()
	}
	stopActivity := func() {
		cancelActivity()
		close(done)
		activityWG.Wait()
	}

	answerAgent := route.Runner
	answer := ""
	var transcript []DebateTurn
	if debate {
		result, err := a.answerWithDebate(ctx, job.ChatID, route.Message, memoryContext)
		stopActivity()
		if err != nil {
			log.Printf("debate failed job=%s chat_id=%d: %s", job.ID, job.ChatID, safeError(err))
			a.finishJobError(ctx, job, route.Runner.Name, "agent 토론 실행에 실패했습니다.", err)
			return
		}
		answer = result.Final
		answerAgent = result.Synthesizer
		transcript = result.Transcript
	} else if liveStream {
		var err error
		answer, err = a.askWithLiveProgress(ctx, job, progressID, streamer, prompt)
		stopActivity()
		if err != nil {
			log.Printf("agent failed job=%s chat_id=%d agent=%s: %s", job.ID, job.ChatID, route.Runner.Name, safeError(err))
			a.finishJobError(ctx, job, route.Runner.Name, "로컬 에이전트 실행에 실패했습니다.", err)
			return
		}
	} else {
		var err error
		answer, err = route.Runner.Agent.Ask(ctx, prompt)
		stopActivity()
		if err != nil {
			log.Printf("agent failed job=%s chat_id=%d agent=%s: %s", job.ID, job.ChatID, route.Runner.Name, safeError(err))
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
		log.Printf("send answer failed chat_id=%d: %s", job.ChatID, safeError(err))
		a.finishJobError(ctx, job, answerAgent.Name, "Telegram 답변 전송에 실패했습니다.", err)
		return
	}
	a.updateJobProgress(context.Background(), job, progressID, formatJobProgress(job, "답변을 전송했습니다.", "기억을 정리 중입니다."), nil)

	if _, err := a.rememberWithAgentID(ctx, answerAgent.Agent, job.ChatID, route.Message, safeAnswer, memoryID); err != nil {
		log.Printf("memory append failed chat_id=%d: %s", job.ChatID, safeError(err))
	}
	a.updateJobProgress(context.Background(), job, progressID, formatJobProgress(job, "작업이 완료되었습니다.", ""), jobDoneActions(job.ID))
	a.jobs.Finish(job, JobSucceeded, answerAgent.Name, "")
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

func jobEnqueueError(err error) string {
	switch {
	case errors.Is(err, ErrRateLimited):
		return "요청이 너무 빠릅니다. 잠시 후 다시 시도하세요."
	case errors.Is(err, ErrQueueFull):
		return "작업 대기열이 가득 찼습니다. 기존 작업이 끝난 뒤 다시 시도하세요."
	default:
		return "작업을 등록하지 못했습니다.\n\n" + redactSecrets(err.Error())
	}
}
