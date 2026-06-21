package app

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
)

type DebateTurn struct {
	Stage     string
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
	allParticipants := a.router.Participants(userMessage, debateMaxAgents)
	synthesizer := a.router.Default()

	// Exclude synthesizer from analysis so it can review without bias toward its own output.
	var participants []AgentParticipant
	for _, p := range allParticipants {
		if p.Runner.Name != synthesizer.Name {
			participants = append(participants, p)
		}
	}

	if len(participants) == 0 {
		prompt := promptWithMemory(memoryContext, userMessage)
		answer, err := synthesizer.Agent.Ask(ctx, prompt)
		if err != nil {
			return DebateResult{}, err
		}
		return DebateResult{
			Final:       answer,
			Synthesizer: synthesizer,
		}, nil
	}

	// analyses + review + synthesis
	total := len(participants) + 2

	// The analyses are independent by design (no agent sees another's output),
	// so run them concurrently and reassemble in participant order afterwards.
	results := make([]*DebateTurn, len(participants))
	var wg sync.WaitGroup
	sem := make(chan struct{}, debateMaxParallel)
	for i, participant := range participants {
		wg.Add(1)
		go func(i int, participant AgentParticipant) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			prompt := buildDebateTurnPrompt(userMessage, memoryContext, participant)
			answer, err := participant.Runner.Agent.Ask(ctx, prompt)
			if err != nil {
				log.Printf("debate turn skipped agent=%s chat_id=%d: %v", participant.Runner.Name, chatID, err)
				return
			}
			results[i] = &DebateTurn{
				Stage:     "analysis",
				AgentName: participant.Runner.Name,
				Backend:   participant.Runner.Backend,
				Content:   strings.TrimSpace(answer),
			}
		}(i, participant)
	}
	wg.Wait()

	var transcript []DebateTurn
	for _, turn := range results {
		if turn == nil {
			continue
		}
		transcript = append(transcript, *turn)
		a.sendDebateTurn(ctx, chatID, *turn, len(transcript), total)
	}

	if len(transcript) == 0 {
		return DebateResult{}, fmt.Errorf("all debate participants failed")
	}

	reviewPrompt := buildDebateReviewPrompt(userMessage, memoryContext, transcript)
	review, err := synthesizer.Agent.Ask(ctx, reviewPrompt)
	if err != nil {
		return DebateResult{}, fmt.Errorf("%s review: %w", synthesizer.Name, err)
	}
	reviewTurn := DebateTurn{
		Stage:     "review",
		AgentName: synthesizer.Name,
		Backend:   synthesizer.Backend,
		Content:   strings.TrimSpace(review),
	}
	transcript = append(transcript, reviewTurn)
	a.sendDebateTurn(ctx, chatID, reviewTurn, len(transcript), total)

	finalPrompt := buildDebateSynthesisPrompt(userMessage, memoryContext, transcript)
	final, err := synthesizer.Agent.Ask(ctx, finalPrompt)
	if err != nil {
		return DebateResult{}, fmt.Errorf("%s synthesis: %w", synthesizer.Name, err)
	}
	synthesisTurn := DebateTurn{
		Stage:     "synthesis",
		AgentName: synthesizer.Name,
		Backend:   synthesizer.Backend,
		Content:   strings.TrimSpace(final),
	}
	transcript = append(transcript, synthesisTurn)
	a.sendDebateTurn(ctx, chatID, synthesisTurn, len(transcript), total)

	return DebateResult{
		Final:       synthesisTurn.Content,
		Synthesizer: synthesizer,
		Transcript:  transcript,
	}, nil
}

func (a *App) sendDebateTurn(ctx context.Context, chatID int64, turn DebateTurn, index, total int) {
	if a.bot == nil {
		return
	}
	message := fmt.Sprintf("[debate %d/%d · %s · %s]\n%s", index, total, turn.Stage, turn.AgentName, turn.Content)
	if err := a.sendMessage(ctx, chatID, message, 0); err != nil {
		log.Printf("send debate transcript failed chat_id=%d agent=%s: %v", chatID, turn.AgentName, err)
	}
}

func buildDebateTurnPrompt(userMessage, memoryContext string, participant AgentParticipant) string {
	var out strings.Builder
	out.WriteString("You are producing an expert analysis for a multi-agent answer.\n")
	out.WriteString("Analyze independently from your assigned role. Do not merely restate the request or imitate other roles.\n")
	out.WriteString("Identify the most important facts, assumptions, uncertainties, risks, and concrete recommendations within your expertise.\n")
	out.WriteString("Do not invent facts or sources. If a claim needs current or external verification, say so explicitly.\n")
	out.WriteString("Do not write the final user-facing answer. Keep the analysis concise and substantive.\n")
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
	if strings.TrimSpace(memoryContext) != "" {
		out.WriteString("\nMemory context:\n")
		out.WriteString(strings.TrimSpace(memoryContext))
		out.WriteByte('\n')
	}
	out.WriteString("\nUser message:\n")
	out.WriteString(strings.TrimSpace(userMessage))
	out.WriteByte('\n')
	out.WriteString("\nYour turn:")
	return out.String()
}

func buildDebateReviewPrompt(userMessage, memoryContext string, transcript []DebateTurn) string {
	var out strings.Builder
	out.WriteString("You are the critical reviewer for a multi-agent answer.\n")
	out.WriteString("Audit the independent analyses before synthesis. Identify contradictions, unsupported claims, missing constraints, safety issues, duplicated advice, and recommendations that do not follow from the evidence.\n")
	out.WriteString("State concrete corrections and which points are reliable enough to keep. Do not write the final answer.\n")
	if strings.TrimSpace(memoryContext) != "" {
		out.WriteString("\nMemory context:\n")
		out.WriteString(strings.TrimSpace(memoryContext))
		out.WriteByte('\n')
	}
	out.WriteString("\nUser message:\n")
	out.WriteString(strings.TrimSpace(userMessage))
	out.WriteString("\n\nIndependent analyses:\n")
	out.WriteString(formatDebateTranscript(transcript))
	out.WriteString("\n\nReview:")
	return out.String()
}

func buildDebateSynthesisPrompt(userMessage, memoryContext string, transcript []DebateTurn) string {
	var out strings.Builder
	out.WriteString("You are the synthesis agent. Read the multi-agent discussion and write the best final Telegram reply to the user.\n")
	out.WriteString("Use only defensible points, apply the review corrections, resolve disagreements, remove repetition, and answer the user's actual objective directly.\n")
	out.WriteString("Separate facts from assumptions or uncertainty. Never invent sources, tool results, or current facts. Include concrete next steps where useful.\n")
	out.WriteString("Do not mention the internal debate unless it is necessary. Keep the answer practical and proportionate to the request.\n")
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
		out.WriteString(fmt.Sprintf("[%s · %s]\n%s\n\n", turn.Stage, turn.AgentName, strings.TrimSpace(turn.Content)))
	}
	return strings.TrimSpace(out.String())
}
