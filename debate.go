package main

import (
	"context"
	"fmt"
	"log"
	"strings"
)

type DebateTurn struct {
	Round     int
	AgentName string
	Backend   string
	Content   string
}

type DebateResult struct {
	Final       string
	Synthesizer AgentRunner
	Transcript  []DebateTurn
}

func (a *App) answerWithDebate(ctx context.Context, chatID int64, userMessage, memoryContext string) (DebateResult, error) {
	participants := a.router.Participants(userMessage, debateMaxAgents)
	if len(participants) == 0 {
		return DebateResult{}, fmt.Errorf("no debate participants are configured")
	}
	if len(participants) == 1 {
		prompt := promptWithMemory(memoryContext, userMessage)
		answer, err := participants[0].Runner.Agent.Ask(ctx, prompt)
		if err != nil {
			return DebateResult{}, err
		}
		return DebateResult{
			Final:       answer,
			Synthesizer: participants[0].Runner,
		}, nil
	}

	rounds := debateRounds

	var transcript []DebateTurn
	for round := 1; round <= rounds; round++ {
		for _, participant := range participants {
			prompt := buildDebateTurnPrompt(userMessage, memoryContext, transcript, participant, round, rounds)
			answer, err := participant.Runner.Agent.Ask(ctx, prompt)
			if err != nil {
				return DebateResult{}, fmt.Errorf("%s debate turn: %w", participant.Runner.Name, err)
			}
			turn := DebateTurn{
				Round:     round,
				AgentName: participant.Runner.Name,
				Backend:   participant.Runner.Backend,
				Content:   strings.TrimSpace(answer),
			}
			transcript = append(transcript, turn)
			a.sendDebateTurn(ctx, chatID, turn, len(transcript), len(participants)*rounds)
		}
	}

	synthesizer := a.router.Default()
	finalPrompt := buildDebateSynthesisPrompt(userMessage, memoryContext, transcript)
	final, err := synthesizer.Agent.Ask(ctx, finalPrompt)
	if err != nil {
		return DebateResult{}, fmt.Errorf("%s synthesis: %w", synthesizer.Name, err)
	}
	return DebateResult{
		Final:       strings.TrimSpace(final),
		Synthesizer: synthesizer,
		Transcript:  transcript,
	}, nil
}

func (a *App) sendDebateTurn(ctx context.Context, chatID int64, turn DebateTurn, index, total int) {
	if a.bot == nil {
		return
	}
	message := fmt.Sprintf("[debate %d/%d · round %d · %s]\n%s", index, total, turn.Round, turn.AgentName, turn.Content)
	if err := a.sendMessage(ctx, chatID, message, 0); err != nil {
		// Debate transcript visibility is useful but should not block the final answer.
		log.Printf("send debate transcript failed chat_id=%d agent=%s: %v", chatID, turn.AgentName, err)
	}
}

func buildDebateTurnPrompt(userMessage, memoryContext string, transcript []DebateTurn, participant AgentParticipant, round, totalRounds int) string {
	var out strings.Builder
	out.WriteString("You are participating in a short multi-agent discussion before the final answer is written.\n")
	out.WriteString("Represent your agent persona and add the most useful perspective, critique, or missing context.\n")
	out.WriteString("Do not write the final answer. Keep your turn concise and concrete.\n")
	out.WriteString(fmt.Sprintf("Agent: %s\n", participant.Runner.Name))
	if participant.Runner.Description != "" {
		out.WriteString("Agent description: ")
		out.WriteString(participant.Runner.Description)
		out.WriteByte('\n')
	}
	if participant.Reason != "" {
		out.WriteString("Selection reason: ")
		out.WriteString(participant.Reason)
		out.WriteByte('\n')
	}
	out.WriteString(fmt.Sprintf("Round: %d of %d\n", round, totalRounds))
	if strings.TrimSpace(memoryContext) != "" {
		out.WriteString("\nMemory context:\n")
		out.WriteString(strings.TrimSpace(memoryContext))
		out.WriteByte('\n')
	}
	out.WriteString("\nUser message:\n")
	out.WriteString(strings.TrimSpace(userMessage))
	out.WriteByte('\n')
	if len(transcript) > 0 {
		out.WriteString("\nDebate so far:\n")
		out.WriteString(formatDebateTranscript(transcript))
		out.WriteByte('\n')
	}
	out.WriteString("\nYour turn:")
	return out.String()
}

func buildDebateSynthesisPrompt(userMessage, memoryContext string, transcript []DebateTurn) string {
	var out strings.Builder
	out.WriteString("You are the synthesis agent. Read the multi-agent discussion and write the best final Telegram reply to the user.\n")
	out.WriteString("Use the strongest points, resolve disagreements, remove repetition, and answer directly.\n")
	out.WriteString("Do not mention internal process unless it is necessary. Keep the answer practical.\n")
	if strings.TrimSpace(memoryContext) != "" {
		out.WriteString("\nMemory context:\n")
		out.WriteString(strings.TrimSpace(memoryContext))
		out.WriteByte('\n')
	}
	out.WriteString("\nUser message:\n")
	out.WriteString(strings.TrimSpace(userMessage))
	out.WriteByte('\n')
	out.WriteString("\nAgent discussion:\n")
	out.WriteString(formatDebateTranscript(transcript))
	out.WriteString("\n\nFinal answer:")
	return out.String()
}

func formatDebateTranscript(transcript []DebateTurn) string {
	var out strings.Builder
	for _, turn := range transcript {
		out.WriteString(fmt.Sprintf("[round %d · %s]\n%s\n\n", turn.Round, turn.AgentName, strings.TrimSpace(turn.Content)))
	}
	return strings.TrimSpace(out.String())
}
