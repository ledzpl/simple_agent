package app

import (
	"context"
	"errors"
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
	// Synthesizer (general/default) is excluded from analysis and only does review + synthesis.
	general := &sequenceAgent{answers: []string{"review notes", "final answer"}}
	coder := &sequenceAgent{answers: []string{"coder view"}}
	critic := &sequenceAgent{answers: []string{"critic view"}}

	router := &AgentRouter{
		defaultIndex: 0,
		runners: []AgentRunner{
			{Name: "general", Description: "general", Match: []string{"*"}, Backend: BackendCodex, Agent: general},
			{Name: "coder", Description: "coder", Match: []string{"코드"}, Backend: BackendCodex, Agent: coder},
			{Name: "critic", Description: "critic", Match: []string{"검토"}, Backend: BackendCodex, Agent: critic},
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
	// 2 analyses (coder, critic) + 1 review + 1 synthesis
	if len(result.Transcript) != 4 {
		t.Fatalf("expected 2 analyses + review + synthesis, got %d turns: %#v", len(result.Transcript), result.Transcript)
	}

	if result.Transcript[0].AgentName != "coder" || result.Transcript[0].Stage != "analysis" || result.Transcript[0].Content != "coder view" {
		t.Fatalf("first analysis mismatch: %#v", result.Transcript[0])
	}
	if result.Transcript[1].AgentName != "critic" || result.Transcript[1].Stage != "analysis" || result.Transcript[1].Content != "critic view" {
		t.Fatalf("second analysis mismatch: %#v", result.Transcript[1])
	}

	// Analyses must be independent — coder's output must not appear in critic's prompt.
	if strings.Contains(critic.prompts[0], "coder view") {
		t.Fatalf("analyses should be independent:\n%s", critic.prompts[0])
	}

	if result.Transcript[2].Stage != "review" || result.Transcript[2].AgentName != "general" || result.Transcript[2].Content != "review notes" {
		t.Fatalf("review turn mismatch: %#v", result.Transcript[2])
	}
	if result.Transcript[3].Stage != "synthesis" || result.Transcript[3].AgentName != "general" || result.Transcript[3].Content != "final answer" {
		t.Fatalf("synthesis turn mismatch: %#v", result.Transcript[3])
	}

	// Review prompt must include all analyses.
	if !strings.Contains(general.prompts[0], "coder view") || !strings.Contains(general.prompts[0], "critic view") {
		t.Fatalf("review prompt missing analyses:\n%s", general.prompts[0])
	}
	// Synthesis prompt must include review and analyses.
	if !strings.Contains(general.prompts[1], "review notes") || !strings.Contains(general.prompts[1], "coder view") {
		t.Fatalf("synthesis prompt missing review or analyses:\n%s", general.prompts[1])
	}

	if result.Synthesizer.Name != "general" {
		t.Fatalf("synthesizer mismatch: %q", result.Synthesizer.Name)
	}
}

func TestAnswerWithDebatePartialFailure(t *testing.T) {
	general := &sequenceAgent{answers: []string{"review notes", "final answer"}}
	coder := &sequenceAgent{answers: []string{"coder view"}}
	failing := &fakeAgent{err: errors.New("timeout")}

	router := &AgentRouter{
		defaultIndex: 0,
		runners: []AgentRunner{
			{Name: "general", Match: []string{"*"}, Backend: BackendCodex, Agent: general},
			{Name: "coder", Match: []string{"코드"}, Backend: BackendCodex, Agent: coder},
			{Name: "failing", Match: []string{"검토"}, Backend: BackendCodex, Agent: failing},
		},
	}
	app := NewAppWithRouter(Config{AgentTimeout: time.Second}, nil, router, nil)

	result, err := app.answerWithDebate(context.Background(), 123, "코드 검토해줘", "")
	if err != nil {
		t.Fatalf("partial failure should not abort debate: %v", err)
	}
	if result.Final != "final answer" {
		t.Fatalf("final answer mismatch: %q", result.Final)
	}
	// coder succeeded, failing skipped → 1 analysis + review + synthesis
	if len(result.Transcript) != 3 {
		t.Fatalf("expected 1 analysis + review + synthesis, got %d turns", len(result.Transcript))
	}
	if result.Transcript[0].AgentName != "coder" {
		t.Fatalf("expected coder as first turn, got %q", result.Transcript[0].AgentName)
	}
}

func TestAnswerWithDebateAllFail(t *testing.T) {
	general := &sequenceAgent{answers: []string{"review notes", "final answer"}}
	failing := &fakeAgent{err: errors.New("timeout")}

	router := &AgentRouter{
		defaultIndex: 0,
		runners: []AgentRunner{
			{Name: "general", Match: []string{"*"}, Backend: BackendCodex, Agent: general},
			{Name: "failing", Match: []string{"코드"}, Backend: BackendCodex, Agent: failing},
		},
	}
	app := NewAppWithRouter(Config{AgentTimeout: time.Second}, nil, router, nil)

	_, err := app.answerWithDebate(context.Background(), 123, "코드 검토해줘", "")
	if err == nil || !strings.Contains(err.Error(), "all debate participants failed") {
		t.Fatalf("expected all-fail error, got %v", err)
	}
}

func TestBuildDebatePrompts(t *testing.T) {
	turn := DebateTurn{Stage: "analysis", AgentName: "critic", Content: "risk"}
	participant := AgentParticipant{
		Runner: AgentRunner{Name: "planner", Description: "plans"},
		Reason: "matched: 계획",
	}

	prompt := buildDebateTurnPrompt("계획 세워줘", "Memory: prior", participant)
	for _, want := range []string{"Agent: planner", "Selection reason: matched: 계획", "Memory: prior", "Your turn:"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("debate turn prompt missing %q:\n%s", want, prompt)
		}
	}
	// Analysis is always independent — no prior transcript injected.
	if strings.Contains(prompt, "[analysis · critic]") {
		t.Fatalf("analysis prompt must not include prior transcript:\n%s", prompt)
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
