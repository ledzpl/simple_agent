package main

import (
	"context"
	"strings"
	"testing"
)

func TestMemoryStoreAppendBuildContextAndClear(t *testing.T) {
	store, err := NewMemoryStore(Config{
		MemoryEnabled:     true,
		MemoryDir:         t.TempDir(),
		MemoryMaxMessages: 10,
		MemoryMaxChars:    1000,
	})
	if err != nil {
		t.Fatalf("NewMemoryStore returned error: %v", err)
	}

	ctx := context.Background()
	if err := store.AppendNote(ctx, 123, "사용자의 이름은 Watson이다."); err != nil {
		t.Fatalf("AppendNote returned error: %v", err)
	}

	memoryContext, err := store.BuildContext(ctx, 123)
	if err != nil {
		t.Fatalf("BuildContext returned error: %v", err)
	}
	for _, want := range []string{"Previous Telegram conversation", "Memory: 사용자의 이름은 Watson이다."} {
		if !strings.Contains(memoryContext, want) {
			t.Fatalf("memory context missing %q:\n%s", want, memoryContext)
		}
	}

	stats, err := store.Stats(ctx, 123)
	if err != nil {
		t.Fatalf("Stats returned error: %v", err)
	}
	if stats.Messages != 1 || stats.Bytes == 0 {
		t.Fatalf("unexpected stats: %#v", stats)
	}

	if err := store.Clear(123); err != nil {
		t.Fatalf("Clear returned error: %v", err)
	}
	stats, err = store.Stats(ctx, 123)
	if err != nil {
		t.Fatalf("Stats after clear returned error: %v", err)
	}
	if stats.Messages != 0 || stats.Bytes != 0 {
		t.Fatalf("memory was not cleared: %#v", stats)
	}
}

func TestLimitMessages(t *testing.T) {
	messages := []MemoryMessage{
		{Role: RoleUser, Content: "one"},
		{Role: RoleAssistant, Content: "two"},
		{Role: RoleUser, Content: "three"},
		{Role: RoleAssistant, Content: "four"},
	}

	limited := limitMessages(messages, 3, 1000)
	if len(limited) != 3 || limited[0].Content != "two" {
		t.Fatalf("max messages limit failed: %#v", limited)
	}

	limited = limitMessages(messages, 10, 9)
	if len(limited) != 2 || limited[0].Content != "three" || limited[1].Content != "four" {
		t.Fatalf("max chars limit failed: %#v", limited)
	}
}

func TestPromptWithMemory(t *testing.T) {
	got := promptWithMemory("User: old", "new")
	if !strings.Contains(got, "User: old") || !strings.Contains(got, "Current Telegram user message:\nnew") {
		t.Fatalf("promptWithMemory mismatch:\n%s", got)
	}
	if got := promptWithMemory("", "new"); got != "new" {
		t.Fatalf("empty memory prompt mismatch: %q", got)
	}
}

func TestMemoryRefinePromptAndNormalize(t *testing.T) {
	prompt := buildMemoryRefinePrompt("내 이름은 Watson이야", "앞으로 Watson이라고 부르겠습니다.", 120)
	for _, want := range []string{"durable memory", "NO_MEMORY", "내 이름은 Watson이야", "앞으로 Watson이라고 부르겠습니다."} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}

	if got := normalizeMemoryNote("```사용자의 이름은 Watson이다.```", 100); got != "사용자의 이름은 Watson이다." {
		t.Fatalf("normalize markdown fence mismatch: %q", got)
	}
	if got := normalizeMemoryNote("NO_MEMORY", 100); got != "" {
		t.Fatalf("NO_MEMORY should be empty: %q", got)
	}
	if got := normalizeMemoryNote("가나다라마", 3); got != "가나다" {
		t.Fatalf("max char truncation mismatch: %q", got)
	}
}
