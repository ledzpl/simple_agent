package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
		MemoryEnabled: true,
		MemoryDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewMemoryStore returned error: %v", err)
	}

	agent := &fakeAgent{answer: "사용자의 이름은 Watson이다."}
	app := NewApp(Config{
		MemoryRefine: true,
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
		MemoryEnabled: true,
		MemoryDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewMemoryStore returned error: %v", err)
	}

	app := NewApp(Config{
		MemoryRefine: true,
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
		MemoryEnabled: true,
		MemoryDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewMemoryStore returned error: %v", err)
	}

	app := NewApp(Config{
		MemoryRefine: true,
	}, nil, &fakeAgent{err: errors.New("boom")}, store)

	err = app.remember(context.Background(), 123, "hello", "answer")
	if err == nil || !strings.Contains(err.Error(), "refine memory") {
		t.Fatalf("expected refine memory error, got %v", err)
	}
}

func TestAnswerActionsUsesMemoryID(t *testing.T) {
	app := NewApp(Config{}, nil, &fakeAgent{}, &MemoryStore{})
	markup := app.answerActions("memory-123", nil)
	if markup == nil || len(markup.InlineKeyboard) != 1 {
		t.Fatalf("expected one action row, got %#v", markup)
	}
	var found bool
	for _, button := range markup.InlineKeyboard[0] {
		if button.CallbackData == "memory_delete:memory-123" {
			found = true
		}
		if button.CallbackData == "memory_delete_last" {
			t.Fatalf("answer actions should not delete the latest memory: %#v", markup)
		}
	}
	if !found {
		t.Fatalf("answer actions missing memory-specific delete callback: %#v", markup)
	}
}

func TestAppRunPersistsTelegramOffset(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/getUpdates":
			if r.URL.Query().Get("offset") == "43" {
				cancel()
				_, _ = w.Write([]byte(`{"ok": true, "result": []}`))
				return
			}
			_, _ = w.Write([]byte(`{
				"ok": true,
				"result": [{
					"update_id": 42,
					"message": {
						"message_id": 7,
						"text": "/id",
						"chat": {"id": 123, "type": "private"},
						"from": {"id": 456}
					}
				}]
			}`))
		case "/sendMessage":
			_, _ = w.Write([]byte(`{"ok": true, "result": {"message_id": 8, "chat": {"id": 123}}}`))
		default:
			t.Fatalf("unexpected Telegram API path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	store, err := NewStateStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStateStore returned error: %v", err)
	}
	bot := &TelegramBot{baseURL: server.URL, client: server.Client()}
	app := NewApp(Config{}, bot, &fakeAgent{answer: "unused"}, nil)
	if err := app.UseStateStore(ctx, store); err != nil {
		t.Fatalf("UseStateStore returned error: %v", err)
	}

	err = app.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context canceled", err)
	}
	offset, err := store.LoadOffset(context.Background())
	if err != nil {
		t.Fatalf("LoadOffset returned error: %v", err)
	}
	if offset != 43 {
		t.Fatalf("persisted offset = %d, want 43", offset)
	}
}
