package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexAgentAskUsesSupportedExecArgs(t *testing.T) {
	dir := t.TempDir()
	fakeCodex := filepath.Join(dir, "fake-codex")
	argsPath := filepath.Join(dir, "args.txt")
	stdinPath := filepath.Join(dir, "stdin.txt")

	script := `#!/bin/sh
printf '%s\n' "$@" > "` + argsPath + `"
cat > "` + stdinPath + `"
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    shift
    out="$1"
    break
  fi
  shift
done
if [ -z "$out" ]; then
  echo "missing -o" >&2
  exit 2
fi
printf 'fake answer\n' > "$out"
`
	if err := os.WriteFile(fakeCodex, []byte(script), 0700); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	agent := CodexAgent{cfg: Config{
		CodexBin:          fakeCodex,
		CodexWorkDir:      dir,
		CodexSandbox:      "read-only",
		AgentTimeout:      time.Second,
		AgentSystemPrompt: "system",
	}}

	answer, err := agent.Ask(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}
	if answer != "fake answer" {
		t.Fatalf("answer mismatch: %q", answer)
	}

	argsData, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	args := string(argsData)
	for _, want := range []string{"exec\n", "--ephemeral\n", "--skip-git-repo-check\n", "--ask-for-approval\n", "never\n", "--sandbox\n", "read-only\n", "-o\n", "-\n"} {
		if !strings.Contains(args, want) {
			t.Fatalf("codex args missing %q in:\n%s", want, args)
		}
	}
	lines := strings.Fields(args)
	if approvalIndex := indexOf(lines, "--ask-for-approval"); approvalIndex < 0 || approvalIndex > indexOf(lines, "exec") {
		t.Fatalf("--ask-for-approval must be passed before exec:\n%s", args)
	}

	stdinData, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("read stdin: %v", err)
	}
	if !strings.Contains(string(stdinData), "system") || !strings.Contains(string(stdinData), "hello") {
		t.Fatalf("prompt not passed through stdin:\n%s", stdinData)
	}
}

func TestCodexAgentDoesNotExposeUnstructuredStdoutAsStreaming(t *testing.T) {
	var agent Agent = CodexAgent{}
	if _, ok := agent.(StreamingAgent); ok {
		t.Fatal("Codex stdout is not a structured answer stream and must not be exposed as live Telegram output")
	}
}

func indexOf(values []string, target string) int {
	for i, value := range values {
		if value == target {
			return i
		}
	}
	return -1
}

func TestOllamaAgentAsk(t *testing.T) {
	var request ollamaChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			t.Fatalf("unexpected content type: %s", r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model": "llama3.2",
			"message": {"role": "assistant", "content": "ollama answer"},
			"done": true
		}`))
	}))
	defer server.Close()

	agent := OllamaAgent{
		cfg: Config{
			AgentTimeout:      time.Second,
			AgentSystemPrompt: "system prompt",
			OllamaURL:         server.URL,
			OllamaModel:       "llama3.2",
		},
		client: server.Client(),
	}

	answer, err := agent.Ask(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}
	if answer != "ollama answer" {
		t.Fatalf("answer mismatch: %q", answer)
	}

	if request.Model != "llama3.2" {
		t.Fatalf("model mismatch: %q", request.Model)
	}
	if request.Stream {
		t.Fatal("expected stream=false")
	}
	if len(request.Messages) != 2 {
		t.Fatalf("messages length mismatch: %#v", request.Messages)
	}
	if request.Messages[0].Role != "system" || request.Messages[0].Content != "system prompt" {
		t.Fatalf("system message mismatch: %#v", request.Messages[0])
	}
	if request.Messages[1].Role != "user" || request.Messages[1].Content != "hello" {
		t.Fatalf("user message mismatch: %#v", request.Messages[1])
	}
}

func TestOllamaAgentAskStream(t *testing.T) {
	var request ollamaChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"Hel"}}`+"\n")
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"lo"},"done":true}`+"\n")
	}))
	defer server.Close()

	agent := OllamaAgent{
		cfg: Config{
			AgentTimeout:      time.Second,
			AgentSystemPrompt: "system prompt",
			OllamaURL:         server.URL,
			OllamaModel:       "llama3.2",
		},
		client: server.Client(),
	}

	var deltas []string
	answer, err := agent.AskStream(context.Background(), "hello", func(d string) {
		deltas = append(deltas, d)
	})
	if err != nil {
		t.Fatalf("AskStream returned error: %v", err)
	}
	if answer != "Hello" {
		t.Fatalf("answer mismatch: %q", answer)
	}
	if !request.Stream {
		t.Fatal("expected stream=true for AskStream")
	}
	if strings.Join(deltas, "") != "Hello" {
		t.Fatalf("delta sequence mismatch: %#v", deltas)
	}
	if len(deltas) != 2 {
		t.Fatalf("expected two streamed chunks, got %d: %#v", len(deltas), deltas)
	}
}

func TestOllamaAgentAskStreamReportsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"error":"model not found"}`+"\n")
	}))
	defer server.Close()

	agent := OllamaAgent{
		cfg: Config{
			AgentTimeout: time.Second,
			OllamaURL:    server.URL,
			OllamaModel:  "missing",
		},
		client: server.Client(),
	}

	_, err := agent.AskStream(context.Background(), "hello", nil)
	if err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("expected ollama error, got %v", err)
	}
}

func TestOllamaAgentAskStreamRejectsTruncatedStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"partial"}}`+"\n")
	}))
	defer server.Close()

	agent := OllamaAgent{
		cfg: Config{
			AgentTimeout: time.Second,
			OllamaURL:    server.URL,
			OllamaModel:  "llama3.2",
		},
		client: server.Client(),
	}

	_, err := agent.AskStream(context.Background(), "hello", nil)
	if err == nil || !strings.Contains(err.Error(), "ended before completion") {
		t.Fatalf("expected truncated stream error, got %v", err)
	}
}

func TestOllamaAgentAskReportsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error": "model not found"}`))
	}))
	defer server.Close()

	agent := OllamaAgent{
		cfg: Config{
			AgentTimeout: time.Second,
			OllamaURL:    server.URL,
			OllamaModel:  "missing",
		},
		client: server.Client(),
	}

	_, err := agent.Ask(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("expected ollama error, got %v", err)
	}
}
