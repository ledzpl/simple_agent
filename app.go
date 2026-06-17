package main

import (
	"context"
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

	debateTranscripts map[string]string
}

func NewApp(cfg Config, bot *TelegramBot, agent Agent, memory *MemoryStore) *App {
	return NewAppWithRouter(cfg, bot, NewSingleAgentRouter(cfg.DefaultAgentName, "Default local agent", []string{"*"}, agent, cfg.AgentBackend), memory)
}

func NewAppWithRouter(cfg Config, bot *TelegramBot, router *AgentRouter, memory *MemoryStore) *App {
	return &App{
		cfg:               cfg,
		bot:               bot,
		router:            router,
		memory:            memory,
		debateTranscripts: map[string]string{},
	}
}

func (a *App) Run(ctx context.Context) error {
	var offset int64
	backoff := time.Second

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
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
			if update.Message != nil {
				a.handleMessage(ctx, *update.Message)
			}
			if update.CallbackQuery != nil {
				a.handleCallback(ctx, *update.CallbackQuery)
			}
		}
	}
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
		_ = a.sendMessage(ctx, msg.Chat.ID, a.identityMessage(msg.Chat.ID), msg.MessageID)
		return
	case "/help":
		_ = a.sendMessage(ctx, msg.Chat.ID, helpMessage(), msg.MessageID)
		return
	case "/agents":
		a.handleAgents(ctx, msg)
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

	if !a.isAllowed(msg.Chat.ID) {
		_ = a.sendMessage(ctx, msg.Chat.ID, denyMessage(msg.Chat.ID), msg.MessageID)
		log.Printf("denied chat_id=%d text=%q", msg.Chat.ID, truncate(text, 80))
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

	route, err := a.routeMessage(text)
	if err != nil {
		_ = a.sendMessage(ctx, msg.Chat.ID, err.Error(), msg.MessageID)
		return
	}

	log.Printf("handling chat_id=%d message_id=%d agent=%s backend=%s route=%s", msg.Chat.ID, msg.MessageID, route.Runner.Name, route.Runner.Backend, route.Reason)

	memoryContext, err := a.memoryContext(ctx, msg.Chat.ID)
	if err != nil {
		log.Printf("memory load failed chat_id=%d: %v", msg.Chat.ID, err)
		_ = a.sendMessage(ctx, msg.Chat.ID, "저장된 대화 기억을 읽지 못했습니다.\n\n"+err.Error(), msg.MessageID)
		return
	}
	prompt := promptWithMemory(memoryContext, route.Message)

	done := make(chan struct{})
	go a.keepTyping(ctx, msg.Chat.ID, done)

	answerAgent := route.Runner
	answer := ""
	var transcript []DebateTurn
	if a.shouldDebate(route, forceDebate) {
		result, err := a.answerWithDebate(ctx, msg.Chat.ID, route.Message, memoryContext)
		close(done)
		if err != nil {
			log.Printf("debate failed chat_id=%d: %v", msg.Chat.ID, err)
			_ = a.sendMessage(ctx, msg.Chat.ID, "agent 토론 실행에 실패했습니다.\n\n"+err.Error(), msg.MessageID)
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
			log.Printf("agent failed chat_id=%d agent=%s: %v", msg.Chat.ID, route.Runner.Name, err)
			_ = a.sendMessage(ctx, msg.Chat.ID, "로컬 에이전트 실행에 실패했습니다.\n\n"+err.Error(), msg.MessageID)
			return
		}
	}

	if strings.TrimSpace(answer) == "" {
		_ = a.sendMessage(ctx, msg.Chat.ID, "agent가 빈 응답을 반환했습니다.", msg.MessageID)
		return
	}

	memoryID := ""
	if a.memory != nil {
		memoryID = newMemoryID()
	}
	if err := a.sendMessageWithMarkup(ctx, msg.Chat.ID, answer, msg.MessageID, a.answerActions(memoryID, transcript)); err != nil {
		log.Printf("send answer failed chat_id=%d: %v", msg.Chat.ID, err)
		return
	}

	if _, err := a.rememberWithAgentID(ctx, answerAgent.Agent, msg.Chat.ID, route.Message, answer, memoryID); err != nil {
		log.Printf("memory append failed chat_id=%d: %v", msg.Chat.ID, err)
	}
}

func (a *App) handleCallback(ctx context.Context, query TelegramCallbackQuery) {
	if query.Message == nil {
		a.answerCallback(ctx, query.ID, "처리할 메시지를 찾지 못했습니다.")
		return
	}
	chatID := query.Message.Chat.ID
	if !a.isAllowed(chatID) {
		a.answerCallback(ctx, query.ID, "허용되지 않은 chat입니다.")
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
	if a.bot == nil {
		return nil
	}
	options := SendMessageOptions{
		ParseMode:   a.cfg.TelegramParseMode,
		ReplyMarkup: markup,
	}
	_, err := a.bot.SendMessageWithOptions(ctx, chatID, text, replyTo, options)
	return err
}

func (a *App) answerActions(memoryID string, transcript []DebateTurn) *InlineKeyboardMarkup {
	if !a.cfg.TelegramAnswerActions {
		return nil
	}
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

func hideMessageActions() *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{
		{Text: "토론 숨기기", CallbackData: "delete_message"},
	}}}
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

func (a *App) isAllowed(chatID int64) bool {
	if a.cfg.AllowAllChats {
		return true
	}
	_, ok := a.cfg.AllowedChatIDs[chatID]
	return ok
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
