package app

import (
	"context"
	"fmt"
	"log"
	"strings"
)

type DebateTurn struct {
	Round     int
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
			visibleTranscript := transcript
			if round == 1 {
				// First-pass analyses stay independent so earlier agents do not anchor later ones.
				visibleTranscript = nil
			}
			prompt := buildDebateTurnPrompt(userMessage, memoryContext, visibleTranscript, participant, round, rounds)
			answer, err := participant.Runner.Agent.Ask(ctx, prompt)
			if err != nil {
				return DebateResult{}, fmt.Errorf("%s debate turn: %w", participant.Runner.Name, err)
			}
			turn := DebateTurn{
				Round:     round,
				Stage:     "analysis",
				AgentName: participant.Runner.Name,
				Backend:   participant.Runner.Backend,
				Content:   strings.TrimSpace(answer),
			}
			transcript = append(transcript, turn)
			a.sendDebateTurn(ctx, chatID, turn, len(transcript), len(participants)*rounds+1)
		}
	}

	synthesizer := a.router.Default()
	reviewPrompt := buildDebateReviewPrompt(userMessage, memoryContext, transcript)
	review, err := synthesizer.Agent.Ask(ctx, reviewPrompt)
	if err != nil {
		return DebateResult{}, fmt.Errorf("%s review: %w", synthesizer.Name, err)
	}
	reviewTurn := DebateTurn{
		Round:     rounds + 1,
		Stage:     "review",
		AgentName: synthesizer.Name,
		Backend:   synthesizer.Backend,
		Content:   strings.TrimSpace(review),
	}
	transcript = append(transcript, reviewTurn)
	a.sendDebateTurn(ctx, chatID, reviewTurn, len(transcript), len(participants)*rounds+1)

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
	stage := turn.Stage
	if stage == "" {
		stage = fmt.Sprintf("round %d", turn.Round)
	}
	message := fmt.Sprintf("[debate %d/%d · %s · %s]\n%s", index, total, stage, turn.AgentName, turn.Content)
	if err := a.sendMessage(ctx, chatID, message, 0); err != nil {
		// Debate transcript visibility is useful but should not block the final answer.
		log.Printf("send debate transcript failed chat_id=%d agent=%s: %v", chatID, turn.AgentName, err)
	}
}

func buildDebateTurnPrompt(userMessage, memoryContext string, transcript []DebateTurn, participant AgentParticipant, round, totalRounds int) string {
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
		stage := turn.Stage
		if stage == "" {
			stage = fmt.Sprintf("round %d", turn.Round)
		}
		out.WriteString(fmt.Sprintf("[%s · %s]\n%s\n\n", stage, turn.AgentName, strings.TrimSpace(turn.Content)))
	}
	return strings.TrimSpace(out.String())
}
