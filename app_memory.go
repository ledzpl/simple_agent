package main

import (
	"context"
	"fmt"
	"time"
)

func (a *App) memoryContext(ctx context.Context, chatID int64) (string, error) {
	if a.memory == nil {
		return "", nil
	}
	return a.memory.BuildContext(ctx, chatID)
}

func (a *App) remember(ctx context.Context, chatID int64, userMessage, assistantMessage string) error {
	return a.rememberWithAgent(ctx, a.router.Default().Agent, chatID, userMessage, assistantMessage)
}

func (a *App) rememberWithAgent(ctx context.Context, agent Agent, chatID int64, userMessage, assistantMessage string) error {
	if a.memory == nil {
		return nil
	}

	note, err := a.refinedMemoryNote(ctx, agent, userMessage, assistantMessage)
	if err != nil {
		return err
	}
	if note == "" {
		return nil
	}
	return a.memory.AppendNote(ctx, chatID, note)
}

func (a *App) refinedMemoryNote(ctx context.Context, agent Agent, userMessage, assistantMessage string) (string, error) {
	if !a.cfg.MemoryRefine {
		return compactMemoryNote(userMessage, assistantMessage, a.cfg.MemoryRefineMax), nil
	}

	timeout := a.cfg.MemoryRefineTime
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	refineCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	prompt := buildMemoryRefinePrompt(userMessage, assistantMessage, a.cfg.MemoryRefineMax)
	note, err := agent.Ask(refineCtx, prompt)
	if err != nil {
		return "", fmt.Errorf("refine memory: %w", err)
	}
	return normalizeMemoryNote(note, a.cfg.MemoryRefineMax), nil
}
