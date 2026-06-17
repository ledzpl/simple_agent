package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

type App struct {
	cfg    Config
	bot    *TelegramBot
	router *AgentRouter
	memory *MemoryStore
}

func NewApp(cfg Config, bot *TelegramBot, agent Agent, memory *MemoryStore) *App {
	return NewAppWithRouter(cfg, bot, NewSingleAgentRouter(cfg.DefaultAgentName, "Default local agent", []string{"*"}, agent, cfg.AgentBackend), memory)
}

func NewAppWithRouter(cfg Config, bot *TelegramBot, router *AgentRouter, memory *MemoryStore) *App {
	return &App{cfg: cfg, bot: bot, router: router, memory: memory}
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
			if update.Message == nil {
				continue
			}
			a.handleMessage(ctx, *update.Message)
		}
	}
}

func (a *App) handleMessage(ctx context.Context, msg TelegramMessage) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		_ = a.bot.SendMessage(ctx, msg.Chat.ID, "텍스트 메시지만 처리할 수 있습니다.", msg.MessageID)
		return
	}

	command := normalizeCommand(text)
	switch command {
	case "/start", "/id":
		_ = a.bot.SendMessage(ctx, msg.Chat.ID, a.identityMessage(msg.Chat.ID), msg.MessageID)
		return
	case "/help":
		_ = a.bot.SendMessage(ctx, msg.Chat.ID, helpMessage(), msg.MessageID)
		return
	case "/agents":
		a.handleAgents(ctx, msg)
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
		_ = a.bot.SendMessage(ctx, msg.Chat.ID, denyMessage(msg.Chat.ID), msg.MessageID)
		log.Printf("denied chat_id=%d text=%q", msg.Chat.ID, truncate(text, 80))
		return
	}

	forceDebate := false
	if normalizeCommand(text) == "/debate" {
		message, ok := parseMessageCommand(text, "/debate")
		if !ok {
			_ = a.bot.SendMessage(ctx, msg.Chat.ID, "사용법: /debate <message>", msg.MessageID)
			return
		}
		text = message
		forceDebate = true
	}

	route, err := a.routeMessage(text)
	if err != nil {
		_ = a.bot.SendMessage(ctx, msg.Chat.ID, err.Error(), msg.MessageID)
		return
	}

	log.Printf("handling chat_id=%d message_id=%d agent=%s backend=%s route=%s", msg.Chat.ID, msg.MessageID, route.Runner.Name, route.Runner.Backend, route.Reason)

	memoryContext, err := a.memoryContext(ctx, msg.Chat.ID)
	if err != nil {
		log.Printf("memory load failed chat_id=%d: %v", msg.Chat.ID, err)
		_ = a.bot.SendMessage(ctx, msg.Chat.ID, "저장된 대화 기억을 읽지 못했습니다.\n\n"+err.Error(), 0)
		return
	}
	prompt := promptWithMemory(memoryContext, route.Message)

	done := make(chan struct{})
	go a.keepTyping(ctx, msg.Chat.ID, done)

	answerAgent := route.Runner
	answer := ""
	if a.shouldDebate(route, forceDebate) {
		result, err := a.answerWithDebate(ctx, msg.Chat.ID, route.Message, memoryContext)
		close(done)
		if err != nil {
			log.Printf("debate failed chat_id=%d: %v", msg.Chat.ID, err)
			_ = a.bot.SendMessage(ctx, msg.Chat.ID, "agent 토론 실행에 실패했습니다.\n\n"+err.Error(), 0)
			return
		}
		answer = result.Final
		answerAgent = result.Synthesizer
	} else {
		var err error
		answer, err = route.Runner.Agent.Ask(ctx, prompt)
		close(done)
		if err != nil {
			log.Printf("agent failed chat_id=%d agent=%s: %v", msg.Chat.ID, route.Runner.Name, err)
			_ = a.bot.SendMessage(ctx, msg.Chat.ID, "로컬 에이전트 실행에 실패했습니다.\n\n"+err.Error(), 0)
			return
		}
	}

	if strings.TrimSpace(answer) == "" {
		_ = a.bot.SendMessage(ctx, msg.Chat.ID, "agent가 빈 응답을 반환했습니다.", 0)
		return
	}

	if err := a.bot.SendMessage(ctx, msg.Chat.ID, answer, 0); err != nil {
		log.Printf("send answer failed chat_id=%d: %v", msg.Chat.ID, err)
		return
	}

	if err := a.rememberWithAgent(ctx, answerAgent.Agent, msg.Chat.ID, route.Message, answer); err != nil {
		log.Printf("memory append failed chat_id=%d: %v", msg.Chat.ID, err)
	}
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
