package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

type sequenceAgent struct {
	answers []string
	prompts []string
}

func (a *sequenceAgent) Ask(ctx context.Context, prompt string) (string, error) {
	a.prompts = append(a.prompts, prompt)
	if len(a.answers) == 0 {
		return "", nil
	}
	answer := a.answers[0]
	a.answers = a.answers[1:]
	return answer, nil
}

func TestAnswerWithDebate(t *testing.T) {
	general := &sequenceAgent{answers: []string{"general view", "review notes", "final answer"}}
	coder := &sequenceAgent{answers: []string{"coder view"}}
	critic := &sequenceAgent{answers: []string{"critic view"}}

	router := &AgentRouter{
		defaultIndex: 0,
		runners: []AgentRunner{
			{Name: "general", Description: "general", Match: []string{"*"}, Backend: BackendCommand, Agent: general},
			{Name: "coder", Description: "coder", Match: []string{"코드"}, Backend: BackendCommand, Agent: coder},
			{Name: "critic", Description: "critic", Match: []string{"검토"}, Backend: BackendCommand, Agent: critic},
		},
	}
	app := NewAppWithRouter(Config{AgentTimeout: time.Second}, nil, router, nil)

	result, err := app.answerWithDebate(context.Background(), 123, "코드 검토해줘", "Memory: prior")
	if err != nil {
		t.Fatalf("answerWithDebate returned error: %v", err)
	}
	if result.Final != "final answer" {
		t.Fatalf("final answer mismatch: %q", result.Final)
	}
	if len(result.Transcript) != 4 {
		t.Fatalf("expected three analyses and one review, got %#v", result.Transcript)
	}
	if result.Transcript[0].AgentName != "coder" || result.Transcript[0].Stage != "analysis" || result.Transcript[0].Content != "coder view" {
		t.Fatalf("first turn mismatch: %#v", result.Transcript[0])
	}
	if strings.Contains(critic.prompts[0], "coder view") {
		t.Fatalf("first-pass analyses should be independent:\n%s", critic.prompts[0])
	}
	if result.Transcript[3].Stage != "review" || result.Transcript[3].Content != "review notes" {
		t.Fatalf("review turn mismatch: %#v", result.Transcript[3])
	}
	if !strings.Contains(general.prompts[1], "coder view") || !strings.Contains(general.prompts[1], "critic view") {
		t.Fatalf("review prompt missing independent analyses:\n%s", general.prompts[1])
	}
	if !strings.Contains(general.prompts[2], "review notes") || !strings.Contains(general.prompts[2], "coder view") {
		t.Fatalf("synthesis prompt missing review or analyses:\n%s", general.prompts[2])
	}
}

func TestBuildDebatePrompts(t *testing.T) {
	turn := DebateTurn{Round: 1, AgentName: "critic", Content: "risk"}
	participant := AgentParticipant{
		Runner: AgentRunner{Name: "planner", Description: "plans"},
		Reason: "matched: 계획",
	}

	prompt := buildDebateTurnPrompt("계획 세워줘", "Memory: prior", []DebateTurn{turn}, participant, 1, 2)
	for _, want := range []string{"Agent: planner", "Selection reason: matched: 계획", "Memory: prior", "risk", "Your turn:"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("debate turn prompt missing %q:\n%s", want, prompt)
		}
	}

	synthesis := buildDebateSynthesisPrompt("계획 세워줘", "Memory: prior", []DebateTurn{turn})
	for _, want := range []string{"synthesis agent", "Memory: prior", "계획 세워줘", "risk", "Final answer:"} {
		if !strings.Contains(synthesis, want) {
			t.Fatalf("synthesis prompt missing %q:\n%s", want, synthesis)
		}
	}

	review := buildDebateReviewPrompt("계획 세워줘", "Memory: prior", []DebateTurn{turn})
	for _, want := range []string{"critical reviewer", "unsupported claims", "Memory: prior", "계획 세워줘", "risk", "Review:"} {
		if !strings.Contains(review, want) {
			t.Fatalf("review prompt missing %q:\n%s", want, review)
		}
	}
}
