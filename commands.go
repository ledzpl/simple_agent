package main

import (
	"context"
	"fmt"
	"strconv"
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
	{Usage: "/route <message>", Description: "메시지가 어떤 agent로 라우팅되는지 점수와 이유 확인"},
	{Usage: "/memory", Description: "현재 chat의 저장된 기억 상태 확인"},
	{Usage: "/memory show", Description: "현재 chat의 저장된 기억 목록 보기"},
	{Usage: "/memory delete <n>", Description: "n번째 저장 기억 삭제"},
	{Usage: "/memory export", Description: "현재 chat의 저장 기억을 JSONL로 내보내기"},
	{Usage: "/memory repair", Description: "손상된 JSONL 라인을 제거하고 기억 파일 복구"},
	{Usage: "/reset", Description: "현재 chat의 저장된 기억 삭제"},
}

func (a *App) handleAgents(ctx context.Context, msg TelegramMessage) {
	if !a.isAllowed(msg.Chat.ID) {
		_ = a.sendMessage(ctx, msg.Chat.ID, denyMessage(msg.Chat.ID), msg.MessageID)
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
	_ = a.sendMessage(ctx, msg.Chat.ID, strings.TrimSpace(out.String()), msg.MessageID)
}

func (a *App) handleMemory(ctx context.Context, msg TelegramMessage) {
	if !a.isAllowed(msg.Chat.ID) {
		_ = a.sendMessage(ctx, msg.Chat.ID, denyMessage(msg.Chat.ID), msg.MessageID)
		return
	}
	if a.memory == nil {
		_ = a.sendMessage(ctx, msg.Chat.ID, "대화 기억이 비활성화되어 있습니다. MEMORY_ENABLED=true로 켤 수 있습니다.", msg.MessageID)
		return
	}

	action, arg := parseMemoryCommand(msg.Text)
	switch action {
	case "status":
		a.handleMemoryStatus(ctx, msg)
	case "show":
		a.handleMemoryShow(ctx, msg)
	case "delete":
		a.handleMemoryDelete(ctx, msg, arg)
	case "export":
		a.handleMemoryExport(ctx, msg)
	case "repair":
		a.handleMemoryRepair(ctx, msg)
	case "help":
		_ = a.sendMessage(ctx, msg.Chat.ID, memoryUsage(), msg.MessageID)
	default:
		_ = a.sendMessage(ctx, msg.Chat.ID, memoryUsage(), msg.MessageID)
	}
}

func (a *App) handleMemoryStatus(ctx context.Context, msg TelegramMessage) {
	stats, err := a.memory.Stats(ctx, msg.Chat.ID)
	if err != nil {
		_ = a.sendMessage(ctx, msg.Chat.ID, "대화 기억 상태를 읽지 못했습니다.\n\n"+err.Error(), msg.MessageID)
		return
	}

	_ = a.sendMessage(ctx, msg.Chat.ID, fmt.Sprintf(
		"대화 기억이 활성화되어 있습니다.\nmessages: %d\ninvalid_lines: %d\nbytes: %d\nfile: %s\nprompt window: last %d messages / %d chars",
		stats.Messages,
		stats.InvalidLines,
		stats.Bytes,
		stats.Path,
		a.cfg.MemoryMaxMessages,
		a.cfg.MemoryMaxChars,
	), msg.MessageID)
}

func (a *App) handleMemoryShow(ctx context.Context, msg TelegramMessage) {
	messages, issues, err := a.memory.LoadDetailed(ctx, msg.Chat.ID)
	if err != nil {
		_ = a.sendMessage(ctx, msg.Chat.ID, "저장된 기억을 읽지 못했습니다.\n\n"+err.Error(), msg.MessageID)
		return
	}
	if len(messages) == 0 {
		_ = a.sendMessage(ctx, msg.Chat.ID, "저장된 기억이 없습니다.", msg.MessageID)
		return
	}

	var out strings.Builder
	out.WriteString("저장된 기억:\n")
	for i, message := range messages {
		out.WriteString("\n")
		out.WriteString(formatMemoryLine(i+1, message))
	}
	if len(issues) > 0 {
		out.WriteString(fmt.Sprintf("\n\n손상된 JSONL 라인 %d개는 건너뛰었습니다. /memory repair 로 제거할 수 있습니다.", len(issues)))
	}
	_ = a.sendMessage(ctx, msg.Chat.ID, strings.TrimSpace(out.String()), msg.MessageID)
}

func (a *App) handleMemoryDelete(ctx context.Context, msg TelegramMessage, arg string) {
	index, err := strconv.Atoi(strings.TrimSpace(arg))
	if err != nil || index <= 0 {
		_ = a.sendMessage(ctx, msg.Chat.ID, "사용법: /memory delete <n>", msg.MessageID)
		return
	}
	if err := a.memory.Delete(msg.Chat.ID, index); err != nil {
		_ = a.sendMessage(ctx, msg.Chat.ID, "저장된 기억을 삭제하지 못했습니다.\n\n"+err.Error(), msg.MessageID)
		return
	}
	_ = a.sendMessage(ctx, msg.Chat.ID, fmt.Sprintf("%d번째 저장 기억을 삭제했습니다.", index), msg.MessageID)
}

func (a *App) handleMemoryExport(ctx context.Context, msg TelegramMessage) {
	exported, issues, err := a.memory.ExportJSONL(ctx, msg.Chat.ID)
	if err != nil {
		_ = a.sendMessage(ctx, msg.Chat.ID, "저장된 기억을 내보내지 못했습니다.\n\n"+err.Error(), msg.MessageID)
		return
	}
	if exported == "" {
		message := "내보낼 저장 기억이 없습니다."
		if len(issues) > 0 {
			message += fmt.Sprintf("\n손상된 JSONL 라인 %d개는 내보내기에서 제외했습니다.", len(issues))
		}
		_ = a.sendMessage(ctx, msg.Chat.ID, message, msg.MessageID)
		return
	}
	_ = a.sendMessage(ctx, msg.Chat.ID, exported, msg.MessageID)
	if len(issues) > 0 {
		_ = a.sendMessage(ctx, msg.Chat.ID, fmt.Sprintf("손상된 JSONL 라인 %d개는 내보내기에서 제외했습니다.", len(issues)), msg.MessageID)
	}
}

func (a *App) handleMemoryRepair(ctx context.Context, msg TelegramMessage) {
	stats, err := a.memory.Repair(ctx, msg.Chat.ID)
	if err != nil {
		_ = a.sendMessage(ctx, msg.Chat.ID, "저장된 기억 파일을 복구하지 못했습니다.\n\n"+err.Error(), msg.MessageID)
		return
	}
	if stats.InvalidLines == 0 {
		_ = a.sendMessage(ctx, msg.Chat.ID, "복구할 손상 라인이 없습니다.", msg.MessageID)
		return
	}
	_ = a.sendMessage(ctx, msg.Chat.ID, fmt.Sprintf("손상된 JSONL 라인 %d개를 제거했습니다.\nmessages: %d\nbytes: %d", stats.InvalidLines, stats.Messages, stats.Bytes), msg.MessageID)
}

func (a *App) handleRoute(ctx context.Context, msg TelegramMessage) {
	if !a.isAllowed(msg.Chat.ID) {
		_ = a.sendMessage(ctx, msg.Chat.ID, denyMessage(msg.Chat.ID), msg.MessageID)
		return
	}
	message, ok := parseMessageCommand(msg.Text, "/route")
	if !ok {
		_ = a.sendMessage(ctx, msg.Chat.ID, "사용법: /route <message>", msg.MessageID)
		return
	}

	route := a.router.Route(message)
	var out strings.Builder
	out.WriteString(fmt.Sprintf("selected: %s\nreason: %s\nscore: %d\n", route.Runner.Name, route.Reason, route.Score))
	out.WriteString("\nall candidates:")
	for _, candidate := range a.router.Scores(message) {
		reason := candidate.Reason
		if reason == "" {
			reason = "no match"
		}
		out.WriteString(fmt.Sprintf("\n- %s: score=%d, %s", candidate.Runner.Name, candidate.Score, reason))
	}
	_ = a.sendMessage(ctx, msg.Chat.ID, out.String(), msg.MessageID)
}

func (a *App) handleReset(ctx context.Context, msg TelegramMessage) {
	if !a.isAllowed(msg.Chat.ID) {
		_ = a.sendMessage(ctx, msg.Chat.ID, denyMessage(msg.Chat.ID), msg.MessageID)
		return
	}
	if a.memory == nil {
		_ = a.sendMessage(ctx, msg.Chat.ID, "대화 기억이 비활성화되어 있습니다.", msg.MessageID)
		return
	}
	if err := a.memory.Clear(msg.Chat.ID); err != nil {
		_ = a.sendMessage(ctx, msg.Chat.ID, "대화 기억을 삭제하지 못했습니다.\n\n"+err.Error(), msg.MessageID)
		return
	}
	_ = a.sendMessage(ctx, msg.Chat.ID, "이 채팅의 저장된 대화 기억을 삭제했습니다.", msg.MessageID)
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

func parseMemoryCommand(text string) (string, string) {
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return "status", ""
	}
	action := strings.ToLower(fields[1])
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), fields[0]))
	rest = strings.TrimSpace(strings.TrimPrefix(rest, fields[1]))
	return action, rest
}

func formatMemoryLine(index int, message MemoryMessage) string {
	created := ""
	if !message.CreatedAt.IsZero() {
		created = " · " + message.CreatedAt.UTC().Format("2006-01-02 15:04:05Z")
	}
	content := strings.Join(strings.Fields(message.Content), " ")
	return fmt.Sprintf("%d. %s%s\n%s", index, message.Role, created, truncate(content, 500))
}

func memoryUsage() string {
	return strings.TrimSpace(`사용법:
/memory - 저장 기억 상태 확인
/memory show - 저장 기억 목록 보기
/memory delete <n> - n번째 저장 기억 삭제
/memory export - 저장 기억을 JSONL로 내보내기
/memory repair - 손상된 JSONL 라인 제거`)
}
