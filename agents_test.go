package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAgentRouterRoutesByMatch(t *testing.T) {
	general := &fakeAgent{answer: "general"}
	coder := &fakeAgent{answer: "coder"}
	router := &AgentRouter{
		defaultIndex: 0,
		runners: []AgentRunner{
			{Name: "general", Description: "general", Match: []string{"*"}, Backend: BackendCommand, Agent: general},
			{Name: "coder", Description: "coding", Match: []string{"코드", "버그"}, Backend: BackendCodex, Agent: coder},
		},
	}

	route := router.Route("이 코드 버그 좀 봐줘")
	if route.Runner.Name != "coder" {
		t.Fatalf("expected coder route, got %#v", route)
	}
	if route.Reason == "" || !strings.Contains(route.Reason, "matched") {
		t.Fatalf("expected match reason, got %q", route.Reason)
	}

	route = router.Route("오늘 일정 정리해줘")
	if route.Runner.Name != "general" || route.Reason != "default" {
		t.Fatalf("expected default route, got %#v", route)
	}
}

func TestAgentRouterParticipants(t *testing.T) {
	router := &AgentRouter{
		defaultIndex: 0,
		runners: []AgentRunner{
			{Name: "general", Match: []string{"*"}, Backend: BackendCommand, Agent: &fakeAgent{}},
			{Name: "coder", Match: []string{"코드"}, Backend: BackendCommand, Agent: &fakeAgent{}},
			{Name: "critic", Match: []string{"검토"}, Backend: BackendCommand, Agent: &fakeAgent{}},
			{Name: "teacher", Match: []string{"설명"}, Backend: BackendCommand, Agent: &fakeAgent{}},
		},
	}

	participants := router.Participants("코드 검토 부탁해", 2)
	if len(participants) != 2 {
		t.Fatalf("expected 2 participants, got %#v", participants)
	}
	if participants[0].Runner.Name != "coder" || participants[1].Runner.Name != "critic" {
		t.Fatalf("unexpected matched participants: %#v", participants)
	}

	participants = router.Participants("잡담", 3)
	if len(participants) != 1 {
		t.Fatalf("expected only default participant without matches, got %#v", participants)
	}
	if participants[0].Runner.Name != "general" {
		t.Fatalf("expected default first without matches, got %#v", participants)
	}
}

func TestAgentRouterRouteTo(t *testing.T) {
	router := NewSingleAgentRouter("general", "general", nil, &fakeAgent{}, BackendCommand)

	route, err := router.RouteTo("GENERAL", "hello")
	if err != nil {
		t.Fatalf("RouteTo returned error: %v", err)
	}
	if !route.Forced || route.Runner.Name != "general" || route.Message != "hello" {
		t.Fatalf("unexpected explicit route: %#v", route)
	}

	if _, err := router.RouteTo("missing", "hello"); err == nil {
		t.Fatal("expected unknown agent error")
	}
}

func TestParseAgentCommand(t *testing.T) {
	name, message, ok := parseAgentCommand("/agent coder 코드 봐줘")
	if !ok {
		t.Fatal("expected parse success")
	}
	if name != "coder" || message != "코드 봐줘" {
		t.Fatalf("unexpected parse result name=%q message=%q", name, message)
	}

	if _, _, ok := parseAgentCommand("/agent coder"); ok {
		t.Fatal("expected parse failure without message")
	}
}

func TestParseMessageCommand(t *testing.T) {
	message, ok := parseMessageCommand("/debate 여러 관점으로 봐줘", "/debate")
	if !ok {
		t.Fatal("expected parse success")
	}
	if message != "여러 관점으로 봐줘" {
		t.Fatalf("message mismatch: %q", message)
	}

	if _, ok := parseMessageCommand("/debate", "/debate"); ok {
		t.Fatal("expected parse failure without message")
	}
}

func TestLoadAgentDefinitionsFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	if err := os.WriteFile(path, []byte(`{
	  "default": "general",
	  "agents": [
	    {
	      "name": "general",
	      "description": "General assistant",
	      "backend": "command",
	      "command": ["/bin/sh", "-c", "cat"],
	      "match": ["*"]
	    },
	    {
	      "name": "local",
	      "description": "Local llama",
	      "backend": "ollama",
	      "ollama_model": "llama3.2",
	      "match": ["로컬"]
	    }
	  ]
	}`), 0600); err != nil {
		t.Fatalf("write agents file: %v", err)
	}

	router, err := NewAgentRouter(Config{
		AgentBackend:      BackendCodex,
		AgentTimeout:      time.Second,
		AgentSystemPrompt: "system",
		AgentsFile:        path,
		DefaultAgentName:  "default",
		CodexBin:          "codex",
		CodexWorkDir:      dir,
		CodexSandbox:      "read-only",
		OllamaURL:         "http://localhost:11434",
	})
	if err != nil {
		t.Fatalf("NewAgentRouter returned error: %v", err)
	}

	if router.Default().Name != "general" {
		t.Fatalf("default agent mismatch: %#v", router.Default())
	}
	if got := router.Route("로컬 모델로 답해줘").Runner.Name; got != "local" {
		t.Fatalf("route mismatch: %q", got)
	}
}

func TestConfigForAgentCommand(t *testing.T) {
	cfg, err := configForAgent(Config{
		AgentBackend:      BackendCodex,
		AgentTimeout:      time.Second,
		AgentSystemPrompt: "base",
		CodexBin:          "codex",
		CodexWorkDir:      ".",
		CodexSandbox:      "read-only",
	}, AgentDefinition{
		Name:         "shell",
		Backend:      BackendCommand,
		SystemPrompt: "agent prompt",
		Command:      []string{"/bin/sh", "-c", "cat"},
	})
	if err != nil {
		t.Fatalf("configForAgent returned error: %v", err)
	}
	if cfg.AgentBackend != BackendCommand || cfg.AgentSystemPrompt != "agent prompt" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	if len(cfg.Command) != 3 || cfg.Command[0] != "/bin/sh" {
		t.Fatalf("command mismatch: %#v", cfg.Command)
	}

	agent, err := NewAgent(cfg)
	if err != nil {
		t.Fatalf("NewAgent returned error: %v", err)
	}
	answer, err := agent.Ask(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}
	if !strings.Contains(answer, "hello") {
		t.Fatalf("answer mismatch: %q", answer)
	}
}

func TestConfigForAgentInheritsDefaultBackend(t *testing.T) {
	cfg, err := configForAgent(Config{
		AgentBackend:      BackendOllama,
		AgentTimeout:      time.Second,
		AgentSystemPrompt: "base",
		OllamaURL:         "http://localhost:11434",
		OllamaModel:       "llama3.2",
	}, AgentDefinition{
		Name:         "teacher",
		SystemPrompt: "teacher prompt",
	})
	if err != nil {
		t.Fatalf("configForAgent returned error: %v", err)
	}
	if cfg.AgentBackend != BackendOllama {
		t.Fatalf("expected inherited backend %q, got %q", BackendOllama, cfg.AgentBackend)
	}
	if cfg.OllamaModel != "llama3.2" {
		t.Fatalf("expected inherited ollama model, got %q", cfg.OllamaModel)
	}
	if cfg.AgentSystemPrompt != "teacher prompt" {
		t.Fatalf("expected agent prompt override, got %q", cfg.AgentSystemPrompt)
	}
}

func TestBundledAgentFilesAreRoleBased(t *testing.T) {
	for _, path := range []string{"agents.json", "agents.example.json"} {
		t.Run(path, func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			var file AgentFile
			if err := json.Unmarshal(data, &file); err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			if file.Default != "moderator" {
				t.Fatalf("default mismatch in %s: %q", path, file.Default)
			}
			roles := map[string]bool{}
			for _, agent := range file.Agents {
				roles[agent.Name] = true
				if agent.Backend != "" {
					t.Fatalf("%s agent %q should inherit global backend, got backend=%q", path, agent.Name, agent.Backend)
				}
			}
			for _, want := range []string{"moderator", "doctor", "lawyer", "politician", "teacher", "engineer", "economist", "psychologist", "journalist"} {
				if !roles[want] {
					t.Fatalf("%s missing role agent %q", path, want)
				}
			}
		})
	}
}
