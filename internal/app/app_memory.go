package app

import (
	"context"
	"fmt"
)

func (a *App) memoryContext(ctx context.Context, chatID int64, query string) (string, error) {
	if a.memory == nil {
		return "", nil
	}
	return a.memory.BuildContextForQuery(ctx, chatID, query)
}

func (a *App) remember(ctx context.Context, chatID int64, userMessage, assistantMessage string) error {
	return a.rememberWithAgent(ctx, a.router.Default().Agent, chatID, userMessage, assistantMessage)
}

func (a *App) rememberWithAgent(ctx context.Context, agent Agent, chatID int64, userMessage, assistantMessage string) error {
	_, err := a.rememberWithAgentID(ctx, agent, chatID, userMessage, assistantMessage, "")
	return err
}

func (a *App) rememberWithAgentID(ctx context.Context, agent Agent, chatID int64, userMessage, assistantMessage, memoryID string) (string, error) {
	if a.memory == nil {
		return "", nil
	}

	note, err := a.refinedMemoryNote(ctx, agent, userMessage, assistantMessage)
	refineErr := err
	memoryID, appendErr := a.memory.AppendExchangeWithNoteID(context.Background(), chatID, memoryID, userMessage, assistantMessage, note)
	if appendErr != nil {
		return "", appendErr
	}
	if refineErr != nil {
		return memoryID, refineErr
	}
	return memoryID, nil
}

func (a *App) refinedMemoryNote(ctx context.Context, agent Agent, userMessage, assistantMessage string) (string, error) {
	if !a.cfg.MemoryRefine {
		return compactMemoryNote(userMessage, assistantMessage, memoryRefineMaxChars), nil
	}

	refineCtx, cancel := context.WithTimeout(ctx, memoryRefineTimeout)
	defer cancel()

	prompt := buildMemoryRefinePrompt(userMessage, assistantMessage, memoryRefineMaxChars)
	note, err := agent.Ask(refineCtx, prompt)
	if err != nil {
		return "", fmt.Errorf("refine memory: %w", err)
	}
	return normalizeMemoryNote(note, memoryRefineMaxChars), nil
}
