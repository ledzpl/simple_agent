package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type AgentDefinition struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Match        []string `json:"match"`
	Examples     []string `json:"examples"`
	Backend      string   `json:"backend"`
	SystemPrompt string   `json:"system_prompt"`
	Timeout      string   `json:"timeout"`

	CodexBin     string `json:"codex_bin"`
	CodexWorkDir string `json:"codex_workdir"`
	CodexModel   string `json:"codex_model"`
	CodexSandbox string `json:"codex_sandbox"`

	Command []string `json:"command"`

	OllamaURL   string `json:"ollama_url"`
	OllamaModel string `json:"ollama_model"`
}

type AgentFile struct {
	Default string            `json:"default"`
	Agents  []AgentDefinition `json:"agents"`
}

type AgentRunner struct {
	Name        string
	Description string
	Match       []string
	Examples    []string
	Backend     string
	Agent       Agent
}

func LoadAgentDefinitions(cfg Config) ([]AgentDefinition, string, error) {
	if cfg.AgentsFile == "" {
		return defaultAgentDefinitions(cfg), defaultAgentName, nil
	}

	data, err := os.ReadFile(cfg.AgentsFile)
	if err != nil {
		if os.IsNotExist(err) && cfg.AgentsFile == "agents.json" {
			return defaultAgentDefinitions(cfg), defaultAgentName, nil
		}
		return nil, "", fmt.Errorf("read AGENTS_FILE: %w", err)
	}

	var file AgentFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, "", fmt.Errorf("parse AGENTS_FILE: %w", err)
	}
	if len(file.Agents) == 0 {
		return nil, "", fmt.Errorf("AGENTS_FILE must define at least one agent")
	}
	defaultName := strings.TrimSpace(file.Default)
	if defaultName == "" {
		defaultName = file.Agents[0].Name
	}
	return file.Agents, defaultName, nil
}

func NewAgentRouter(cfg Config) (*AgentRouter, error) {
	defs, defaultName, err := LoadAgentDefinitions(cfg)
	if err != nil {
		return nil, err
	}

	router := &AgentRouter{defaultIndex: -1}
	seen := map[string]struct{}{}
	for _, def := range defs {
		def.Name = normalizeAgentName(def.Name)
		if def.Name == "" {
			return nil, fmt.Errorf("agent name is required")
		}
		if _, ok := seen[def.Name]; ok {
			return nil, fmt.Errorf("duplicate agent name %q", def.Name)
		}
		seen[def.Name] = struct{}{}

		agentCfg, err := configForAgent(cfg, def)
		if err != nil {
			return nil, fmt.Errorf("agent %q: %w", def.Name, err)
		}
		agent, err := NewAgent(agentCfg)
		if err != nil {
			return nil, fmt.Errorf("agent %q: %w", def.Name, err)
		}

		runner := AgentRunner{
			Name:        def.Name,
			Description: strings.TrimSpace(def.Description),
			Match:       normalizeMatches(def.Match),
			Examples:    normalizeExamples(def.Examples),
			Backend:     agentCfg.AgentBackend,
			Agent:       agent,
		}
		if runner.Description == "" {
			runner.Description = runner.Backend + " agent"
		}
		router.runners = append(router.runners, runner)
		if runner.Name == normalizeAgentName(defaultName) {
			router.defaultIndex = len(router.runners) - 1
		}
	}

	if router.defaultIndex < 0 {
		return nil, fmt.Errorf("default agent %q is not defined", defaultName)
	}
	return router, nil
}

func configForAgent(base Config, def AgentDefinition) (Config, error) {
	cfg := base
	if value := strings.TrimSpace(def.Backend); value != "" {
		cfg.AgentBackend = value
	}
	if value := strings.TrimSpace(def.SystemPrompt); value != "" {
		cfg.AgentSystemPrompt = value
	}
	if value := strings.TrimSpace(def.Timeout); value != "" {
		duration, err := parseDurationValue(value)
		if err != nil {
			return Config{}, fmt.Errorf("invalid timeout: %w", err)
		}
		cfg.AgentTimeout = duration
	}

	if value := strings.TrimSpace(def.CodexBin); value != "" {
		cfg.CodexBin = value
	}
	if value := strings.TrimSpace(def.CodexWorkDir); value != "" {
		abs, err := filepath.Abs(value)
		if err != nil {
			return Config{}, fmt.Errorf("resolve codex_workdir: %w", err)
		}
		cfg.CodexWorkDir = abs
	}
	if value := strings.TrimSpace(def.CodexModel); value != "" {
		cfg.CodexModel = value
	}
	if value := strings.TrimSpace(def.CodexSandbox); value != "" {
		cfg.CodexSandbox = value
	}
	if def.Command != nil {
		cfg.Command = append([]string(nil), def.Command...)
	}

	if value := strings.TrimSpace(def.OllamaURL); value != "" {
		cfg.OllamaURL = value
	}
	if value := strings.TrimSpace(def.OllamaModel); value != "" {
		cfg.OllamaModel = value
	}
	if err := validateAgentConfig(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func defaultAgentDefinitions(cfg Config) []AgentDefinition {
	return []AgentDefinition{{
		Name:         defaultAgentName,
		Description:  "Default local agent",
		Backend:      cfg.AgentBackend,
		SystemPrompt: cfg.AgentSystemPrompt,
		Match:        []string{"*"},
	}}
}

func normalizeAgentName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	name = strings.TrimPrefix(name, "@")
	return name
}

func normalizeMatches(matches []string) []string {
	out := make([]string, 0, len(matches))
	seen := map[string]struct{}{}
	for _, match := range matches {
		match = strings.TrimSpace(strings.ToLower(match))
		if match == "" {
			continue
		}
		if _, ok := seen[match]; ok {
			continue
		}
		seen[match] = struct{}{}
		out = append(out, match)
	}
	sort.Strings(out)
	return out
}

func normalizeExamples(examples []string) []string {
	out := make([]string, 0, len(examples))
	seen := map[string]struct{}{}
	for _, example := range examples {
		example = strings.TrimSpace(example)
		if example == "" {
			continue
		}
		key := strings.ToLower(example)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, example)
	}
	return out
}
