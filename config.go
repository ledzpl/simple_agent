package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	BackendCodex   = "codex"
	BackendCommand = "command"
	BackendOllama  = "ollama"
)

type Config struct {
	TelegramToken        string
	AllowedChatIDs       map[int64]struct{}
	AllowAllChats        bool
	AgentBackend         string
	AgentTimeout         time.Duration
	AgentSystemPrompt    string
	AgentsFile           string
	AgentsFileExplicit   bool
	DefaultAgentName     string
	DebateEnabled        bool
	DebateMaxAgents      int
	DebateRounds         int
	DebateShowTranscript bool
	MemoryEnabled        bool
	MemoryDir            string
	MemoryMaxMessages    int
	MemoryMaxChars       int
	MemoryRefine         bool
	MemoryRefineMax      int
	MemoryRefineTime     time.Duration

	CodexBin       string
	CodexWorkDir   string
	CodexModel     string
	CodexSandbox   string
	CodexExtraArgs []string

	Command []string

	OllamaURL       string
	OllamaModel     string
	OllamaKeepAlive string
}

func LoadConfig() (Config, error) {
	if err := LoadDotEnv(".env"); err != nil {
		return Config{}, err
	}

	cfg := Config{
		TelegramToken:        strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")),
		AllowAllChats:        envBool("TELEGRAM_ALLOW_ALL", false),
		AgentBackend:         envDefault("AGENT_BACKEND", BackendCodex),
		AgentTimeout:         envDuration("AGENT_TIMEOUT", 5*time.Minute),
		AgentSystemPrompt:    envDefault("AGENT_SYSTEM_PROMPT", defaultSystemPrompt()),
		AgentsFile:           envDefault("AGENTS_FILE", "agents.json"),
		AgentsFileExplicit:   strings.TrimSpace(os.Getenv("AGENTS_FILE")) != "",
		DefaultAgentName:     envDefault("DEFAULT_AGENT", "default"),
		DebateEnabled:        envBool("DEBATE_ENABLED", false),
		DebateMaxAgents:      envInt("DEBATE_MAX_AGENTS", 4),
		DebateRounds:         envInt("DEBATE_ROUNDS", 1),
		DebateShowTranscript: envBool("DEBATE_SHOW_TRANSCRIPT", true),
		MemoryEnabled:        envBool("MEMORY_ENABLED", true),
		MemoryDir:            envDefault("MEMORY_DIR", ".telegram-memory"),
		MemoryMaxMessages:    envInt("MEMORY_MAX_MESSAGES", 20),
		MemoryMaxChars:       envInt("MEMORY_MAX_CHARS", 12000),
		MemoryRefine:         envBool("MEMORY_REFINE", true),
		MemoryRefineMax:      envInt("MEMORY_REFINE_MAX_CHARS", 1000),
		MemoryRefineTime:     envDuration("MEMORY_REFINE_TIMEOUT", 90*time.Second),
		CodexBin:             envDefault("CODEX_BIN", "codex"),
		CodexWorkDir:         envDefault("CODEX_WORKDIR", "."),
		CodexSandbox:         envDefault("CODEX_SANDBOX", "read-only"),
		CodexModel:           strings.TrimSpace(os.Getenv("CODEX_MODEL")),
		OllamaURL:            envDefault("OLLAMA_URL", "http://localhost:11434"),
		OllamaModel:          strings.TrimSpace(os.Getenv("OLLAMA_MODEL")),
		OllamaKeepAlive:      strings.TrimSpace(os.Getenv("OLLAMA_KEEP_ALIVE")),
	}

	if cfg.TelegramToken == "" {
		return Config{}, errors.New("TELEGRAM_BOT_TOKEN is required")
	}

	chatIDs, err := parseAllowedChatIDs(os.Getenv("TELEGRAM_ALLOWED_CHAT_IDS"))
	if err != nil {
		return Config{}, err
	}
	cfg.AllowedChatIDs = chatIDs

	if cfg.CodexWorkDir != "" {
		abs, err := filepath.Abs(cfg.CodexWorkDir)
		if err != nil {
			return Config{}, fmt.Errorf("resolve CODEX_WORKDIR: %w", err)
		}
		cfg.CodexWorkDir = abs
	}

	if cfg.MemoryDir != "" {
		abs, err := filepath.Abs(cfg.MemoryDir)
		if err != nil {
			return Config{}, fmt.Errorf("resolve MEMORY_DIR: %w", err)
		}
		cfg.MemoryDir = abs
	}

	if raw := strings.TrimSpace(os.Getenv("CODEX_EXTRA_ARGS")); raw != "" {
		args, err := splitCommandLine(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse CODEX_EXTRA_ARGS: %w", err)
		}
		cfg.CodexExtraArgs = args
	}

	if cfg.AgentBackend == BackendCommand {
		raw := strings.TrimSpace(os.Getenv("LOCAL_AGENT_COMMAND"))
		if raw == "" {
			return Config{}, errors.New("LOCAL_AGENT_COMMAND is required when AGENT_BACKEND=command")
		}
		args, err := splitCommandLine(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse LOCAL_AGENT_COMMAND: %w", err)
		}
		if len(args) == 0 {
			return Config{}, errors.New("LOCAL_AGENT_COMMAND is empty")
		}
		cfg.Command = args
	}

	if err := validateAgentConfig(cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func validateAgentConfig(cfg Config) error {
	if cfg.AgentTimeout <= 0 {
		return errors.New("AGENT_TIMEOUT must be positive")
	}
	switch cfg.AgentBackend {
	case BackendCodex:
		if strings.TrimSpace(cfg.CodexBin) == "" {
			return errors.New("CODEX_BIN is required when backend is codex")
		}
	case BackendCommand:
		if len(cfg.Command) == 0 {
			return errors.New("LOCAL_AGENT_COMMAND or agent command is required when backend is command")
		}
	case BackendOllama:
		if strings.TrimSpace(cfg.OllamaModel) == "" {
			return errors.New("OLLAMA_MODEL or agent ollama_model is required when backend is ollama")
		}
	default:
		return fmt.Errorf("unsupported backend %q; use %q, %q, or %q", cfg.AgentBackend, BackendCodex, BackendCommand, BackendOllama)
	}
	return nil
}

func defaultSystemPrompt() string {
	return "You are answering a message received from Telegram. Keep the answer concise and practical. If you need to modify files or run commands, explain that this Telegram bridge is configured for a local agent and may be sandboxed."
}

func envDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	switch value {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return fallback
	}
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	duration, err := parseDurationValue(value)
	if err == nil && duration > 0 {
		return duration
	}
	return fallback
}

func parseDurationValue(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errors.New("duration is empty")
	}
	duration, err := time.ParseDuration(value)
	if err == nil {
		return duration, nil
	}
	seconds, err := strconv.Atoi(value)
	if err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second, nil
	}
	return 0, fmt.Errorf("invalid duration %q", value)
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func parseAllowedChatIDs(raw string) (map[int64]struct{}, error) {
	ids := map[int64]struct{}{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ids, nil
	}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid TELEGRAM_ALLOWED_CHAT_IDS value %q: %w", part, err)
		}
		ids[id] = struct{}{}
	}
	return ids, nil
}
