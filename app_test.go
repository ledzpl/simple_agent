package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeAgent struct {
	answer  string
	err     error
	prompts []string
}

func (a *fakeAgent) Ask(ctx context.Context, prompt string) (string, error) {
	a.prompts = append(a.prompts, prompt)
	if a.err != nil {
		return "", a.err
	}
	return a.answer, nil
}

func TestAppRememberStoresRefinedNote(t *testing.T) {
	store, err := NewMemoryStore(Config{
		MemoryEnabled:     true,
		MemoryDir:         t.TempDir(),
		MemoryMaxMessages: 10,
		MemoryMaxChars:    1000,
	})
	if err != nil {
		t.Fatalf("NewMemoryStore returned error: %v", err)
	}

	agent := &fakeAgent{answer: "사용자의 이름은 Watson이다."}
	app := NewApp(Config{
		MemoryRefine:     true,
		MemoryRefineMax:  1000,
		MemoryRefineTime: time.Second,
	}, nil, agent, store)

	if err := app.remember(context.Background(), 123, "안녕 내 이름은 Watson이야", "알겠습니다."); err != nil {
		t.Fatalf("remember returned error: %v", err)
	}
	if len(agent.prompts) != 1 {
		t.Fatalf("expected one refine prompt, got %d", len(agent.prompts))
	}
	if !strings.Contains(agent.prompts[0], "Summarize this Telegram exchange") {
		t.Fatalf("unexpected refine prompt:\n%s", agent.prompts[0])
	}

	messages, err := store.Load(context.Background(), 123)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected one memory note, got %#v", messages)
	}
	if messages[0].Role != RoleMemory || messages[0].Content != "사용자의 이름은 Watson이다." {
		t.Fatalf("unexpected stored memory: %#v", messages[0])
	}
	if strings.Contains(messages[0].Content, "안녕") || strings.Contains(messages[0].Content, "알겠습니다") {
		t.Fatalf("stored raw exchange instead of refined note: %#v", messages[0])
	}
}

func TestAppRememberSkipsNoMemory(t *testing.T) {
	store, err := NewMemoryStore(Config{
		MemoryEnabled:     true,
		MemoryDir:         t.TempDir(),
		MemoryMaxMessages: 10,
		MemoryMaxChars:    1000,
	})
	if err != nil {
		t.Fatalf("NewMemoryStore returned error: %v", err)
	}

	app := NewApp(Config{
		MemoryRefine:     true,
		MemoryRefineMax:  1000,
		MemoryRefineTime: time.Second,
	}, nil, &fakeAgent{answer: "NO_MEMORY"}, store)

	if err := app.remember(context.Background(), 123, "고마워", "천만에요."); err != nil {
		t.Fatalf("remember returned error: %v", err)
	}
	messages, err := store.Load(context.Background(), 123)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("expected no memory to be stored, got %#v", messages)
	}
}

func TestAppRememberReturnsRefineError(t *testing.T) {
	store, err := NewMemoryStore(Config{
		MemoryEnabled:     true,
		MemoryDir:         t.TempDir(),
		MemoryMaxMessages: 10,
		MemoryMaxChars:    1000,
	})
	if err != nil {
		t.Fatalf("NewMemoryStore returned error: %v", err)
	}

	app := NewApp(Config{
		MemoryRefine:     true,
		MemoryRefineMax:  1000,
		MemoryRefineTime: time.Second,
	}, nil, &fakeAgent{err: errors.New("boom")}, store)

	err = app.remember(context.Background(), 123, "hello", "answer")
	if err == nil || !strings.Contains(err.Error(), "refine memory") {
		t.Fatalf("expected refine memory error, got %v", err)
	}
}
