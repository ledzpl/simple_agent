package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

type Agent interface {
	Ask(ctx context.Context, prompt string) (string, error)
}

func NewAgent(cfg Config) (Agent, error) {
	switch cfg.AgentBackend {
	case BackendCodex:
		return CodexAgent{cfg: cfg}, nil
	case BackendCommand:
		return CommandAgent{cfg: cfg}, nil
	case BackendOllama:
		return OllamaAgent{
			cfg:    cfg,
			client: &http.Client{Timeout: cfg.AgentTimeout},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported backend %q", cfg.AgentBackend)
	}
}

type CodexAgent struct {
	cfg Config
}

func (a CodexAgent) Ask(ctx context.Context, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, a.cfg.AgentTimeout)
	defer cancel()

	outputFile, err := os.CreateTemp("", "telegram-codex-last-message-*.txt")
	if err != nil {
		return "", fmt.Errorf("create temp output file: %w", err)
	}
	outputPath := outputFile.Name()
	_ = outputFile.Close()
	defer os.Remove(outputPath)

	args := []string{
		"exec",
		"--skip-git-repo-check",
		"--color", "never",
		"--sandbox", a.cfg.CodexSandbox,
		"-C", a.cfg.CodexWorkDir,
		"-o", outputPath,
	}
	if a.cfg.CodexModel != "" {
		args = append(args, "-m", a.cfg.CodexModel)
	}
	args = append(args, "-")

	cmd := exec.CommandContext(ctx, a.cfg.CodexBin, args...)
	cmd.Stdin = strings.NewReader(buildPrompt(a.cfg.AgentSystemPrompt, prompt))

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("codex timed out after %s", a.cfg.AgentTimeout)
		}
		return "", fmt.Errorf("codex failed: %w%s", err, formatStderr(stderr.String()))
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		return "", fmt.Errorf("read codex output: %w", err)
	}
	answer := strings.TrimSpace(string(data))
	if answer == "" {
		return "", fmt.Errorf("codex returned an empty answer%s", formatStderr(stderr.String()))
	}
	return answer, nil
}

type CommandAgent struct {
	cfg Config
}

func (a CommandAgent) Ask(ctx context.Context, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, a.cfg.AgentTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, a.cfg.Command[0], a.cfg.Command[1:]...)
	cmd.Stdin = strings.NewReader(buildPrompt(a.cfg.AgentSystemPrompt, prompt))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("local command timed out after %s", a.cfg.AgentTimeout)
		}
		return "", fmt.Errorf("local command failed: %w%s", err, formatStderr(stderr.String()))
	}

	answer := strings.TrimSpace(stdout.String())
	if answer == "" {
		return "", fmt.Errorf("local command returned an empty answer%s", formatStderr(stderr.String()))
	}
	return answer, nil
}

type OllamaAgent struct {
	cfg    Config
	client *http.Client
}

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaChatResponse struct {
	Message ollamaMessage `json:"message"`
	Done    bool          `json:"done"`
	Error   string        `json:"error"`
}

func (a OllamaAgent) Ask(ctx context.Context, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, a.cfg.AgentTimeout)
	defer cancel()

	payload := ollamaChatRequest{
		Model: a.cfg.OllamaModel,
		Messages: []ollamaMessage{
			{Role: "system", Content: strings.TrimSpace(a.cfg.AgentSystemPrompt)},
			{Role: "user", Content: strings.TrimSpace(prompt)},
		},
		Stream: false,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode ollama request: %w", err)
	}

	endpoint := strings.TrimRight(a.cfg.OllamaURL, "/") + "/api/chat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("create ollama request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := a.client
	if client == nil {
		client = &http.Client{Timeout: a.cfg.AgentTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("ollama timed out after %s", a.cfg.AgentTimeout)
		}
		return "", fmt.Errorf("ollama request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read ollama response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("ollama status %d: %s", resp.StatusCode, trimBody(body))
	}

	var decoded ollamaChatResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", fmt.Errorf("decode ollama response: %w", err)
	}
	if decoded.Error != "" {
		return "", fmt.Errorf("ollama error: %s", decoded.Error)
	}

	answer := strings.TrimSpace(decoded.Message.Content)
	if answer == "" {
		return "", fmt.Errorf("ollama returned an empty answer")
	}
	return answer, nil
}

func buildPrompt(systemPrompt, userPrompt string) string {
	systemPrompt = strings.TrimSpace(systemPrompt)
	userPrompt = strings.TrimSpace(userPrompt)
	if systemPrompt == "" {
		return userPrompt
	}
	return systemPrompt + "\n\nUser message:\n" + userPrompt + "\n"
}

func formatStderr(stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}
	if len(stderr) > 1200 {
		stderr = stderr[:1200] + "\n..."
	}
	return "\nstderr:\n" + stderr
}
