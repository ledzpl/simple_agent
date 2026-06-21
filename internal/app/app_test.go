package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// slowStreamingAgent emits its deltas with a delay so streaming-progress edits
// have time to fire during a test.
type slowStreamingAgent struct {
	deltas []string
	gap    time.Duration
}

func (a *slowStreamingAgent) Ask(ctx context.Context, prompt string) (string, error) {
	return a.AskStream(ctx, prompt, nil)
}

func (a *slowStreamingAgent) AskStream(ctx context.Context, prompt string, onDelta func(string)) (string, error) {
	var b strings.Builder
	for _, delta := range a.deltas {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(a.gap):
		}
		b.WriteString(delta)
		if onDelta != nil {
			onDelta(delta)
		}
	}
	return b.String(), nil
}

func TestAskWithLiveProgressStreamsPreview(t *testing.T) {
	old := streamEditInterval
	streamEditInterval = 10 * time.Millisecond
	defer func() { streamEditInterval = old }()

	var mu sync.Mutex
	var edits []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/editMessageText" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			edits = append(edits, fmt.Sprint(body["text"]))
			mu.Unlock()
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":1}}}`))
	}))
	defer server.Close()

	bot := &TelegramBot{baseURL: server.URL, client: server.Client()}
	app := NewApp(Config{}, bot, &fakeAgent{}, nil)
	agent := &slowStreamingAgent{deltas: []string{"Hel", "lo", " world"}, gap: 15 * time.Millisecond}
	job := &AgentJob{ID: "j1", ChatID: 1}

	answer, err := app.askWithLiveProgress(context.Background(), job, 99, agent, "hi")
	if err != nil {
		t.Fatalf("askWithLiveProgress returned error: %v", err)
	}
	if answer != "Hello world" {
		t.Fatalf("answer mismatch: %q", answer)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(edits) == 0 {
		t.Fatal("expected at least one streamed preview edit")
	}
	var sawPreview bool
	for _, edit := range edits {
		if strings.Contains(edit, "Hel") {
			sawPreview = true
		}
	}
	if !sawPreview {
		t.Fatalf("preview edits never contained streamed text: %#v", edits)
	}
}

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
	if len(messages) != 3 {
		t.Fatalf("expected raw exchange and one refined memory note, got %#v", messages)
	}
	if messages[0].Role != RoleUser || messages[0].Content != "안녕 내 이름은 Watson이야" {
		t.Fatalf("unexpected stored user message: %#v", messages[0])
	}
	if messages[1].Role != RoleAssistant || messages[1].Content != "알겠습니다." {
		t.Fatalf("unexpected stored assistant message: %#v", messages[1])
	}
	if messages[2].Role != RoleMemory || messages[2].Content != "사용자의 이름은 Watson이다." {
		t.Fatalf("unexpected refined memory: %#v", messages[2])
	}
	for _, message := range messages {
		if message.ID == "" || message.ID != messages[0].ID {
			t.Fatalf("exchange entries must share one id: %#v", messages)
		}
	}
}

func TestAppRememberKeepsRawExchangeWhenNoDurableMemory(t *testing.T) {
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
	if len(messages) != 2 || messages[0].Role != RoleUser || messages[1].Role != RoleAssistant {
		t.Fatalf("expected only raw exchange to be stored, got %#v", messages)
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
	messages, loadErr := store.Load(context.Background(), 123)
	if loadErr != nil {
		t.Fatalf("Load returned error: %v", loadErr)
	}
	if len(messages) != 2 || messages[0].Content != "hello" || messages[1].Content != "answer" {
		t.Fatalf("raw exchange should survive refine failure: %#v", messages)
	}
}

func TestAppRememberPreservesQuizChoicesForShortFollowUp(t *testing.T) {
	store, err := NewMemoryStore(Config{
		MemoryEnabled: true,
		MemoryDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewMemoryStore returned error: %v", err)
	}
	app := NewApp(Config{MemoryRefine: true}, nil, &fakeAgent{
		answer: "퀴즈를 시작했으며 1번 과학 문제의 답을 기다리고 있다.",
	}, store)

	question := "원소기호 W를 사용하는 원소는?\nA. 텅스텐\nB. 주석\nC. 티타늄\nD. 텔루륨"
	if err := app.remember(context.Background(), 123, "퀴즈 내줘", question); err != nil {
		t.Fatalf("remember returned error: %v", err)
	}
	contextText, err := app.memoryContext(context.Background(), 123, "D")
	if err != nil {
		t.Fatalf("memoryContext returned error: %v", err)
	}
	for _, want := range []string{"Assistant:", "원소기호 W", "A. 텅스텐", "D. 텔루륨"} {
		if !strings.Contains(contextText, want) {
			t.Fatalf("short follow-up context missing %q:\n%s", want, contextText)
		}
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

func TestDebateTranscriptStoreIsConcurrentSafe(t *testing.T) {
	app := NewApp(Config{}, nil, &fakeAgent{}, nil)
	const count = 100
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := app.storeDebateTranscript("transcript")
			if got := app.loadDebateTranscript(id); got != "transcript" {
				t.Errorf("loadDebateTranscript(%q) = %q", id, got)
			}
		}()
	}
	wg.Wait()
}
