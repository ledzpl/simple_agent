package app

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
	BackendCodex  = "codex"
	BackendOllama = "ollama"

	defaultAgentName     = "default"
	maxActiveJobsPerChat = 1
	maxActiveJobsGlobal  = 4
	maxQueuedJobsPerChat = 10
	maxQueuedJobsGlobal  = 100
	maxRequestsPerMinute = 10
	jobHistoryLimit      = 20
	jobProgressInterval  = 60 * time.Second
	debateMaxAgents   = 4
	debateMaxParallel = 4
	streamPreviewMaxRunes = 1500
	memoryMaxMessages = 20
	memoryMaxChars       = 12000
	memoryRefineMaxChars = 1000
	memoryRefineTimeout  = 90 * time.Second
)

// streamEditInterval throttles how often a streamed answer preview edits the
// Telegram progress message. It is a var so tests can shorten it.
var streamEditInterval = 2 * time.Second

type Config struct {
	TelegramToken     string
	AllowedChatIDs    map[int64]struct{}
	AllowedUserIDs    map[int64]struct{}
	AllowGroupChats   bool
	AgentBackend      string
	AgentTimeout      time.Duration
	AgentSystemPrompt string
	AgentsFile        string
	DebateEnabled     bool
	MemoryEnabled     bool
	MemoryDir         string
	MemoryRefine      bool
	StateDir          string

	CodexBin     string
	CodexWorkDir string
	CodexModel   string
	CodexSandbox string

	OllamaURL   string
	OllamaModel string
}

func LoadConfig() (Config, error) {
	if err := LoadDotEnv(envDefault("ENV_FILE", ".env")); err != nil {
		return Config{}, err
	}

	allowGroupChats, err := envBool("TELEGRAM_ALLOW_GROUPS", false)
	if err != nil {
		return Config{}, err
	}
	debateEnabled, err := envBool("DEBATE_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	memoryEnabled, err := envBool("MEMORY_ENABLED", true)
	if err != nil {
		return Config{}, err
	}
	memoryRefine, err := envBool("MEMORY_REFINE", true)
	if err != nil {
		return Config{}, err
	}
	agentTimeout, err := envDuration("AGENT_TIMEOUT", 5*time.Minute)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		TelegramToken:     strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")),
		AllowGroupChats:   allowGroupChats,
		AgentBackend:      envDefault("AGENT_BACKEND", BackendCodex),
		AgentTimeout:      agentTimeout,
		AgentSystemPrompt: envDefault("AGENT_SYSTEM_PROMPT", defaultSystemPrompt()),
		AgentsFile:        envDefault("AGENTS_FILE", "agents.json"),
		DebateEnabled:     debateEnabled,
		MemoryEnabled:     memoryEnabled,
		MemoryDir:         envDefault("MEMORY_DIR", ".telegram-memory"),
		MemoryRefine:      memoryRefine,
		StateDir:          envDefault("STATE_DIR", ".telegram-state"),
		CodexBin:          envDefault("CODEX_BIN", "codex"),
		CodexWorkDir:      envDefault("CODEX_WORKDIR", "."),
		CodexSandbox:      normalizeCodexSandbox(envDefault("CODEX_SANDBOX", "read-only")),
		CodexModel:        strings.TrimSpace(os.Getenv("CODEX_MODEL")),
		OllamaURL:         envDefault("OLLAMA_URL", "http://localhost:11434"),
		OllamaModel:       strings.TrimSpace(os.Getenv("OLLAMA_MODEL")),
	}

	if cfg.TelegramToken == "" {
		return Config{}, errors.New("TELEGRAM_BOT_TOKEN is required")
	}

	chatIDs, err := parseAllowedChatIDs(os.Getenv("TELEGRAM_ALLOWED_CHAT_IDS"))
	if err != nil {
		return Config{}, err
	}
	cfg.AllowedChatIDs = chatIDs

	userIDs, err := parseAllowedUserIDs(os.Getenv("TELEGRAM_ALLOWED_USER_IDS"))
	if err != nil {
		return Config{}, err
	}
	cfg.AllowedUserIDs = userIDs

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

	if cfg.StateDir != "" {
		abs, err := filepath.Abs(cfg.StateDir)
		if err != nil {
			return Config{}, fmt.Errorf("resolve STATE_DIR: %w", err)
		}
		cfg.StateDir = abs
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
		switch cfg.CodexSandbox {
		case "read-only", "workspace-write":
		case "danger-full-access":
			return errors.New("CODEX_SANDBOX=danger-full-access is not allowed for a Telegram-triggered agent")
		default:
			return fmt.Errorf("unsupported CODEX_SANDBOX %q; use read-only or workspace-write", cfg.CodexSandbox)
		}
	case BackendOllama:
		if strings.TrimSpace(cfg.OllamaModel) == "" {
			return errors.New("OLLAMA_MODEL or agent ollama_model is required when backend is ollama")
		}
	default:
		return fmt.Errorf("unsupported backend %q; use %q or %q", cfg.AgentBackend, BackendCodex, BackendOllama)
	}
	return nil
}

func defaultSystemPrompt() string {
	return "You are answering a message received from Telegram. Keep the answer concise and practical. If you need to modify files or run commands, explain that this Telegram bridge is configured for a local agent and may be sandboxed."
}

func normalizeCodexSandbox(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "seatbelt" {
		return "read-only"
	}
	return value
}

func envDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) (bool, error) {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if value == "" {
		return fallback, nil
	}
	switch value {
	case "1", "true", "yes", "y", "on":
		return true, nil
	case "0", "false", "no", "n", "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid %s value %q; use true or false", key, value)
	}
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	duration, err := parseDurationValue(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("invalid %s value %q: %w", key, value, err)
	}
	return duration, nil
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

func parseAllowedChatIDs(raw string) (map[int64]struct{}, error) {
	return parseInt64Set(raw, "TELEGRAM_ALLOWED_CHAT_IDS")
}

func parseAllowedUserIDs(raw string) (map[int64]struct{}, error) {
	return parseInt64Set(raw, "TELEGRAM_ALLOWED_USER_IDS")
}

func parseInt64Set(raw, key string) (map[int64]struct{}, error) {
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
			return nil, fmt.Errorf("invalid %s value %q: %w", key, part, err)
		}
		ids[id] = struct{}{}
	}
	return ids, nil
}

