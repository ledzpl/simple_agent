package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type telegramRequestRecorder struct {
	mu       sync.Mutex
	requests map[string][]map[string]any
}

func newTelegramTestServer(t *testing.T) (*httptest.Server, *telegramRequestRecorder) {
	t.Helper()
	recorder := &telegramRequestRecorder{requests: map[string][]map[string]any{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if r.Body != nil {
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode %s payload: %v", r.URL.Path, err)
			}
		}
		recorder.mu.Lock()
		recorder.requests[r.URL.Path] = append(recorder.requests[r.URL.Path], payload)
		count := len(recorder.requests[r.URL.Path])
		recorder.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/sendMessage" {
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":` + strconv.Itoa(count) + `,"chat":{"id":123,"type":"private"}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	return server, recorder
}

func (r *telegramRequestRecorder) texts(path string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, payload := range r.requests[path] {
		if text, ok := payload["text"].(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func TestAgentJobEndToEndSendsAnswerAndStoresMemory(t *testing.T) {
	server, recorder := newTelegramTestServer(t)
	defer server.Close()

	store, err := NewMemoryStore(Config{MemoryEnabled: true, MemoryDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewMemoryStore returned error: %v", err)
	}
	agent := &fakeAgent{answer: "최종 답변"}
	cfg := Config{
		AllowedChatIDs: map[int64]struct{}{123: {}},
		MemoryRefine:   false,
	}
	bot := &TelegramBot{baseURL: server.URL, client: server.Client()}
	app := NewApp(cfg, bot, agent, store)

	snapshot, err := app.enqueueAgentJob(context.Background(), TelegramMessage{
		MessageID: 10,
		Text:      "질문",
		Chat:      TelegramChat{ID: 123, Type: "private"},
		From:      &TelegramUser{ID: 7},
	}, "질문", false)
	if err != nil {
		t.Fatalf("enqueueAgentJob returned error: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, job := range app.jobs.Status(123) {
			if job.ID == snapshot.ID && job.State == JobSucceeded {
				goto completed
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not complete: %#v", app.jobs.Status(123))
		}
		time.Sleep(10 * time.Millisecond)
	}

completed:
	if len(agent.prompts) != 1 || !strings.Contains(agent.prompts[0], "질문") {
		t.Fatalf("agent prompt mismatch: %#v", agent.prompts)
	}
	var sentAnswer bool
	for _, text := range recorder.texts("/sendMessage") {
		if text == "최종 답변" {
			sentAnswer = true
		}
	}
	if !sentAnswer {
		t.Fatalf("Telegram answer was not sent: %#v", recorder.texts("/sendMessage"))
	}
	memories, err := store.Load(context.Background(), 123)
	if err != nil {
		t.Fatalf("Load memory returned error: %v", err)
	}
	if len(memories) != 3 ||
		memories[0].Role != RoleUser ||
		memories[1].Role != RoleAssistant ||
		memories[2].Role != RoleMemory ||
		!strings.Contains(memories[0].Content, "질문") ||
		!strings.Contains(memories[1].Content, "최종 답변") {
		t.Fatalf("stored memory mismatch: %#v", memories)
	}
}

func TestMemoryDeleteCallbackEndToEnd(t *testing.T) {
	server, _ := newTelegramTestServer(t)
	defer server.Close()

	store, err := NewMemoryStore(Config{MemoryEnabled: true, MemoryDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewMemoryStore returned error: %v", err)
	}
	if _, err := store.AppendNoteWithID(context.Background(), 123, "memory-1", "remember me"); err != nil {
		t.Fatalf("AppendNoteWithID returned error: %v", err)
	}
	cfg := Config{AllowedChatIDs: map[int64]struct{}{123: {}}}
	bot := &TelegramBot{baseURL: server.URL, client: server.Client()}
	app := NewApp(cfg, bot, &fakeAgent{}, store)

	app.handleCallback(context.Background(), TelegramCallbackQuery{
		ID:   "callback-1",
		From: &TelegramUser{ID: 7},
		Message: &TelegramMessage{
			MessageID: 20,
			Chat:      TelegramChat{ID: 123, Type: "private"},
		},
		Data: "memory_delete:memory-1",
	})

	memories, err := store.Load(context.Background(), 123)
	if err != nil {
		t.Fatalf("Load memory returned error: %v", err)
	}
	if len(memories) != 0 {
		t.Fatalf("memory delete callback retained memories: %#v", memories)
	}
}
