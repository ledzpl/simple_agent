package main

import (
	"context"
	"fmt"
	"strings"
)

type commandHelp struct {
	Usage       string
	Description string
}

var commandHelps = []commandHelp{
	{Usage: "/start", Description: "연결 상태와 현재 chat id 확인"},
	{Usage: "/id", Description: "현재 Telegram chat id 확인"},
	{Usage: "/help", Description: "사용 가능한 명령 목록 보기"},
	{Usage: "/agents", Description: "사용 가능한 agent와 match 규칙 확인"},
	{Usage: "/agent <name> <message>", Description: "특정 agent로 단독 응답 받기"},
	{Usage: "/debate <message>", Description: "여러 agent 토론 후 최종 답변 받기"},
	{Usage: "/memory", Description: "현재 chat의 저장된 기억 상태 확인"},
	{Usage: "/reset", Description: "현재 chat의 저장된 기억 삭제"},
}

func (a *App) handleAgents(ctx context.Context, msg TelegramMessage) {
	if !a.isAllowed(msg.Chat.ID) {
		_ = a.bot.SendMessage(ctx, msg.Chat.ID, denyMessage(msg.Chat.ID), msg.MessageID)
		return
	}

	var out strings.Builder
	out.WriteString("사용 가능한 agents:\n")
	for _, runner := range a.router.Runners() {
		marker := " "
		if runner.Name == a.router.Default().Name {
			marker = "*"
		}
		out.WriteString(fmt.Sprintf("\n%s %s (%s)\n%s", marker, runner.Name, runner.Backend, runner.Description))
		if len(runner.Match) > 0 {
			out.WriteString("\nmatch: ")
			out.WriteString(strings.Join(runner.Match, ", "))
		}
		out.WriteByte('\n')
	}
	out.WriteString("\n강제 선택: /agent <agent-name> <message>")
	out.WriteString("\n강제 토론: /debate <message>")
	_ = a.bot.SendMessage(ctx, msg.Chat.ID, strings.TrimSpace(out.String()), msg.MessageID)
}

func (a *App) handleMemory(ctx context.Context, msg TelegramMessage) {
	if !a.isAllowed(msg.Chat.ID) {
		_ = a.bot.SendMessage(ctx, msg.Chat.ID, denyMessage(msg.Chat.ID), msg.MessageID)
		return
	}
	if a.memory == nil {
		_ = a.bot.SendMessage(ctx, msg.Chat.ID, "대화 기억이 비활성화되어 있습니다. MEMORY_ENABLED=true로 켤 수 있습니다.", msg.MessageID)
		return
	}

	stats, err := a.memory.Stats(ctx, msg.Chat.ID)
	if err != nil {
		_ = a.bot.SendMessage(ctx, msg.Chat.ID, "대화 기억 상태를 읽지 못했습니다.\n\n"+err.Error(), msg.MessageID)
		return
	}

	_ = a.bot.SendMessage(ctx, msg.Chat.ID, fmt.Sprintf(
		"대화 기억이 활성화되어 있습니다.\nmessages: %d\nbytes: %d\nfile: %s\nprompt window: last %d messages / %d chars",
		stats.Messages,
		stats.Bytes,
		stats.Path,
		a.cfg.MemoryMaxMessages,
		a.cfg.MemoryMaxChars,
	), msg.MessageID)
}

func (a *App) handleReset(ctx context.Context, msg TelegramMessage) {
	if !a.isAllowed(msg.Chat.ID) {
		_ = a.bot.SendMessage(ctx, msg.Chat.ID, denyMessage(msg.Chat.ID), msg.MessageID)
		return
	}
	if a.memory == nil {
		_ = a.bot.SendMessage(ctx, msg.Chat.ID, "대화 기억이 비활성화되어 있습니다.", msg.MessageID)
		return
	}
	if err := a.memory.Clear(msg.Chat.ID); err != nil {
		_ = a.bot.SendMessage(ctx, msg.Chat.ID, "대화 기억을 삭제하지 못했습니다.\n\n"+err.Error(), msg.MessageID)
		return
	}
	_ = a.bot.SendMessage(ctx, msg.Chat.ID, "이 채팅의 저장된 대화 기억을 삭제했습니다.", msg.MessageID)
}

func (a *App) identityMessage(chatID int64) string {
	if a.isAllowed(chatID) {
		return fmt.Sprintf("연결되었습니다.\nchat id: %d\ndefault agent: %s\nagents: %d", chatID, a.router.Default().Name, len(a.router.Runners()))
	}
	return fmt.Sprintf("chat id: %d\n\n이 채팅에서 로컬 에이전트를 실행하려면 서버의 TELEGRAM_ALLOWED_CHAT_IDS에 이 값을 추가하세요.", chatID)
}

func helpMessage() string {
	var out strings.Builder
	out.WriteString("사용 가능한 명령:\n")
	for _, command := range commandHelps {
		out.WriteString("\n")
		out.WriteString(command.Usage)
		out.WriteString(" - ")
		out.WriteString(command.Description)
	}
	out.WriteString("\n\n일반 텍스트 메시지는 허용된 chat에서 로컬 agent로 전달됩니다.")
	out.WriteString("\n로컬 실행은 TELEGRAM_ALLOWED_CHAT_IDS에 등록된 chat id에서만 허용됩니다.")
	return out.String()
}

func denyMessage(chatID int64) string {
	return fmt.Sprintf("이 채팅은 허용 목록에 없습니다.\nchat id: %d\n\n서버의 TELEGRAM_ALLOWED_CHAT_IDS에 이 값을 추가한 뒤 다시 실행하세요.", chatID)
}
