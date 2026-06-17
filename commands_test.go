package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestHelpMessageIncludesDefinedCommands(t *testing.T) {
	help := helpMessage()
	for _, command := range commandHelps {
		if !strings.Contains(help, command.Usage) {
			t.Fatalf("help message missing command %q:\n%s", command.Usage, help)
		}
		if !strings.Contains(help, command.Description) {
			t.Fatalf("help message missing description %q:\n%s", command.Description, help)
		}
	}
}

func TestParseMemoryCommand(t *testing.T) {
	action, arg := parseMemoryCommand("/memory delete 3")
	if action != "delete" || arg != "3" {
		t.Fatalf("unexpected delete parse: action=%q arg=%q", action, arg)
	}

	action, arg = parseMemoryCommand("/memory")
	if action != "status" || arg != "" {
		t.Fatalf("unexpected status parse: action=%q arg=%q", action, arg)
	}
}

func TestMemoryUsageMentionsSubcommands(t *testing.T) {
	usage := memoryUsage()
	for _, want := range []string{"/memory show", "/memory delete <n>", "/memory export", "/memory repair"} {
		if !strings.Contains(usage, want) {
			t.Fatalf("memory usage missing %q:\n%s", want, usage)
		}
	}
}

func TestMemoryExportKeepsJSONLSeparateFromWarning(t *testing.T) {
	var texts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sendMessage" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		texts = append(texts, payload["text"].(string))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer server.Close()

	store, err := NewMemoryStore(Config{
		MemoryEnabled:     true,
		MemoryDir:         t.TempDir(),
		MemoryMaxMessages: 10,
		MemoryMaxChars:    1000,
	})
	if err != nil {
		t.Fatalf("NewMemoryStore returned error: %v", err)
	}
	data := `{"role":"memory","content":"valid","created_at":"2026-06-17T00:00:00Z"}
not json
`
	if err := os.WriteFile(store.path(123), []byte(data), 0600); err != nil {
		t.Fatalf("write memory file: %v", err)
	}

	app := NewAppWithRouter(
		Config{AllowedChatIDs: map[int64]struct{}{123: {}}},
		&TelegramBot{baseURL: server.URL, client: server.Client()},
		NewSingleAgentRouter("default", "default", []string{"*"}, &fakeAgent{}, BackendCommand),
		store,
	)
	app.handleMemoryExport(context.Background(), TelegramMessage{MessageID: 7, Chat: TelegramChat{ID: 123}})

	if len(texts) != 2 {
		t.Fatalf("expected export and warning as separate messages, got %#v", texts)
	}
	if strings.Contains(texts[0], "손상된 JSONL") {
		t.Fatalf("export message should remain JSONL only: %q", texts[0])
	}
	if !strings.Contains(texts[1], "손상된 JSONL 라인 1개") {
		t.Fatalf("warning message mismatch: %#v", texts)
	}
}
