package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleMemory    = "memory"
)

type MemoryMessage struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type MemoryStats struct {
	Path     string
	Messages int
	Bytes    int64
}

type MemoryStore struct {
	dir         string
	maxMessages int
	maxChars    int
}

func NewMemoryStore(cfg Config) (*MemoryStore, error) {
	if !cfg.MemoryEnabled {
		return nil, nil
	}
	if err := os.MkdirAll(cfg.MemoryDir, 0700); err != nil {
		return nil, fmt.Errorf("create memory dir: %w", err)
	}
	return &MemoryStore{
		dir:         cfg.MemoryDir,
		maxMessages: cfg.MemoryMaxMessages,
		maxChars:    cfg.MemoryMaxChars,
	}, nil
}

func (s *MemoryStore) Load(ctx context.Context, chatID int64) ([]MemoryMessage, error) {
	if s == nil {
		return nil, nil
	}

	file, err := os.Open(s.path(chatID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open memory: %w", err)
	}
	defer file.Close()

	var messages []MemoryMessage
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var message MemoryMessage
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			return nil, fmt.Errorf("decode memory line: %w", err)
		}
		if message.Role == "" || message.Content == "" {
			continue
		}
		messages = append(messages, message)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read memory: %w", err)
	}
	return messages, nil
}

func (s *MemoryStore) BuildContext(ctx context.Context, chatID int64) (string, error) {
	messages, err := s.Load(ctx, chatID)
	if err != nil {
		return "", err
	}
	if len(messages) == 0 {
		return "", nil
	}

	messages = limitMessages(messages, s.maxMessages, s.maxChars)
	if len(messages) == 0 {
		return "", nil
	}

	var out strings.Builder
	out.WriteString("Previous Telegram conversation for this chat. Use it as memory and context; do not repeat it unless useful.\n")
	for _, message := range messages {
		switch message.Role {
		case RoleMemory:
			out.WriteString("\nMemory: ")
		case RoleUser:
			out.WriteString("\nUser: ")
		case RoleAssistant:
			out.WriteString("\nAssistant: ")
		default:
			out.WriteString("\n")
			out.WriteString(message.Role)
			out.WriteString(": ")
		}
		out.WriteString(strings.TrimSpace(message.Content))
		out.WriteByte('\n')
	}
	return strings.TrimSpace(out.String()), nil
}

func (s *MemoryStore) AppendNote(ctx context.Context, chatID int64, content string) error {
	if s == nil {
		return nil
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	file, err := os.OpenFile(s.path(chatID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("open memory for append: %w", err)
	}
	defer file.Close()

	entry := MemoryMessage{
		Role:      RoleMemory,
		Content:   strings.TrimSpace(content),
		CreatedAt: time.Now().UTC(),
	}
	if entry.Content == "" {
		return nil
	}

	encoder := json.NewEncoder(file)
	if err := encoder.Encode(entry); err != nil {
		return fmt.Errorf("write memory: %w", err)
	}
	return nil
}

func (s *MemoryStore) Clear(chatID int64) error {
	if s == nil {
		return nil
	}
	if err := os.Remove(s.path(chatID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear memory: %w", err)
	}
	return nil
}

func (s *MemoryStore) Stats(ctx context.Context, chatID int64) (MemoryStats, error) {
	if s == nil {
		return MemoryStats{}, nil
	}

	path := s.path(chatID)
	messages, err := s.Load(ctx, chatID)
	if err != nil {
		return MemoryStats{}, err
	}

	var bytes int64
	if stat, err := os.Stat(path); err == nil {
		bytes = stat.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return MemoryStats{}, fmt.Errorf("stat memory: %w", err)
	}

	return MemoryStats{Path: path, Messages: len(messages), Bytes: bytes}, nil
}

func (s *MemoryStore) path(chatID int64) string {
	return filepath.Join(s.dir, fmt.Sprintf("%d.jsonl", chatID))
}

func limitMessages(messages []MemoryMessage, maxMessages, maxChars int) []MemoryMessage {
	if maxMessages > 0 && len(messages) > maxMessages {
		messages = messages[len(messages)-maxMessages:]
	}
	if maxChars <= 0 {
		return messages
	}

	total := 0
	start := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		total += len([]rune(messages[i].Content))
		if total > maxChars {
			break
		}
		start = i
	}
	if start == len(messages) {
		return nil
	}
	return messages[start:]
}

func promptWithMemory(memoryContext, userPrompt string) string {
	userPrompt = strings.TrimSpace(userPrompt)
	memoryContext = strings.TrimSpace(memoryContext)
	if memoryContext == "" {
		return userPrompt
	}
	return memoryContext + "\n\nCurrent Telegram user message:\n" + userPrompt
}

func buildMemoryRefinePrompt(userMessage, assistantMessage string, maxChars int) string {
	return fmt.Sprintf(`Summarize this Telegram exchange into durable memory for future replies.

Rules:
- Keep only stable facts, user preferences, decisions, constraints, names, IDs, task state, or promised follow-ups.
- Remove filler, greetings, repeated context, transient wording, and full code/output unless the exact detail is important.
- Use the user's language when possible.
- Return NO_MEMORY if there is nothing worth remembering.
- Keep the result within %d characters.
- Return only the memory text, with no heading.

User message:
%s

Assistant response:
%s`, maxChars, strings.TrimSpace(userMessage), strings.TrimSpace(assistantMessage))
}

func normalizeMemoryNote(note string, maxChars int) string {
	note = strings.TrimSpace(note)
	note = strings.Trim(note, "`")
	note = strings.TrimSpace(note)
	if strings.EqualFold(note, "NO_MEMORY") || note == "" {
		return ""
	}
	if maxChars <= 0 {
		return note
	}
	runes := []rune(note)
	if len(runes) <= maxChars {
		return note
	}
	return strings.TrimSpace(string(runes[:maxChars]))
}

func compactMemoryNote(userMessage, assistantMessage string, maxChars int) string {
	note := strings.TrimSpace(fmt.Sprintf("User asked: %s\nAssistant answered: %s", compactLine(userMessage), compactLine(assistantMessage)))
	return normalizeMemoryNote(note, maxChars)
}

func compactLine(text string) string {
	text = strings.TrimSpace(text)
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= 300 {
		return text
	}
	return string(runes[:300]) + "..."
}
