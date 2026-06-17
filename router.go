package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

type AgentRoute struct {
	Runner  AgentRunner
	Message string
	Forced  bool
	Reason  string
	Score   int
}

type AgentParticipant struct {
	Runner  AgentRunner
	Score   int
	Reason  string
	Blocked bool
	index   int
}

type routeEvaluation struct {
	Score   int
	Reason  string
	Blocked bool
}

type AgentRouter struct {
	runners      []AgentRunner
	defaultIndex int
}

func NewSingleAgentRouter(name, description string, match []string, agent Agent, backend string) *AgentRouter {
	name = normalizeAgentName(name)
	if name == "" {
		name = "default"
	}
	if description == "" {
		description = backend + " agent"
	}
	return &AgentRouter{
		defaultIndex: 0,
		runners: []AgentRunner{{
			Name:        name,
			Description: description,
			Match:       normalizeMatches(match),
			Backend:     backend,
			Agent:       agent,
		}},
	}
}

func (r *AgentRouter) Route(message string) AgentRoute {
	if r == nil || len(r.runners) == 0 {
		return AgentRoute{}
	}

	lower := strings.ToLower(message)
	bestIndex := r.defaultIndex
	bestScore := 0
	bestReason := "default"
	if evaluateRoute(lower, r.runners[r.defaultIndex]).Blocked {
		bestIndex = -1
		bestReason = ""
	}
	for i, runner := range r.runners {
		evaluation := evaluateRoute(lower, runner)
		if evaluation.Blocked {
			continue
		}
		if bestIndex < 0 || evaluation.Score > bestScore {
			bestScore = evaluation.Score
			bestIndex = i
			bestReason = evaluation.Reason
		}
	}
	if bestIndex < 0 {
		bestIndex = r.defaultIndex
		bestReason = "all candidates blocked; default fallback"
	}
	if bestReason == "" {
		if bestIndex == r.defaultIndex {
			bestReason = "default"
		} else {
			bestReason = "fallback"
		}
	}

	return AgentRoute{
		Runner:  r.runners[bestIndex],
		Message: message,
		Reason:  bestReason,
		Score:   bestScore,
	}
}

func (r *AgentRouter) Participants(message string, maxAgents int) []AgentParticipant {
	if r == nil || len(r.runners) == 0 {
		return nil
	}
	if maxAgents <= 0 || maxAgents > len(r.runners) {
		maxAgents = len(r.runners)
	}

	lower := strings.ToLower(message)
	candidates := make([]AgentParticipant, 0, len(r.runners))
	for i, runner := range r.runners {
		evaluation := evaluateRoute(lower, runner)
		candidates = append(candidates, AgentParticipant{
			Runner:  runner,
			Score:   evaluation.Score,
			Reason:  evaluation.Reason,
			Blocked: evaluation.Blocked,
			index:   i,
		})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].index < candidates[j].index
	})

	selected := make([]AgentParticipant, 0, maxAgents)
	used := map[string]struct{}{}
	for _, candidate := range candidates {
		if candidate.Blocked || candidate.Score <= 0 {
			continue
		}
		selected = append(selected, candidate)
		used[candidate.Runner.Name] = struct{}{}
		if len(selected) == maxAgents {
			return selected
		}
	}

	if len(selected) == 0 {
		if !evaluateRoute(lower, r.runners[r.defaultIndex]).Blocked {
			return []AgentParticipant{{
				Runner: r.runners[r.defaultIndex],
				Reason: "default participant",
				index:  r.defaultIndex,
			}}
		}
		for _, candidate := range candidates {
			if !candidate.Blocked {
				candidate.Reason = "fallback participant"
				return []AgentParticipant{candidate}
			}
		}
		return []AgentParticipant{{
			Runner: r.runners[r.defaultIndex],
			Reason: "all candidates blocked; default participant",
			index:  r.defaultIndex,
		}}
	}

	if _, ok := used[r.runners[r.defaultIndex].Name]; !ok && len(selected) < maxAgents && !evaluateRoute(lower, r.runners[r.defaultIndex]).Blocked {
		selected = append(selected, AgentParticipant{
			Runner: r.runners[r.defaultIndex],
			Reason: "default participant",
			index:  r.defaultIndex,
		})
	}

	return selected
}

func (r *AgentRouter) Scores(message string) []AgentParticipant {
	if r == nil || len(r.runners) == 0 {
		return nil
	}
	lower := strings.ToLower(message)
	candidates := make([]AgentParticipant, 0, len(r.runners))
	for i, runner := range r.runners {
		evaluation := evaluateRoute(lower, runner)
		reason := evaluation.Reason
		if evaluation.Score == 0 && !evaluation.Blocked && i == r.defaultIndex {
			reason = "default fallback"
		}
		candidates = append(candidates, AgentParticipant{
			Runner:  runner,
			Score:   evaluation.Score,
			Reason:  reason,
			Blocked: evaluation.Blocked,
			index:   i,
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].index < candidates[j].index
	})
	return candidates
}

func (r *AgentRouter) RouteTo(name, message string) (AgentRoute, error) {
	if r == nil || len(r.runners) == 0 {
		return AgentRoute{}, errors.New("no agents are configured")
	}
	name = normalizeAgentName(name)
	for _, runner := range r.runners {
		if runner.Name == name {
			return AgentRoute{Runner: runner, Message: message, Forced: true, Reason: "explicit"}, nil
		}
	}
	return AgentRoute{}, fmt.Errorf("unknown agent %q", name)
}

func (r *AgentRouter) Default() AgentRunner {
	if r == nil || len(r.runners) == 0 {
		return AgentRunner{}
	}
	return r.runners[r.defaultIndex]
}

func (r *AgentRouter) Runners() []AgentRunner {
	if r == nil {
		return nil
	}
	out := make([]AgentRunner, len(r.runners))
	copy(out, r.runners)
	return out
}

func routeScore(lowerMessage string, runner AgentRunner) (int, string) {
	evaluation := evaluateRoute(lowerMessage, runner)
	return evaluation.Score, evaluation.Reason
}

func evaluateRoute(lowerMessage string, runner AgentRunner) routeEvaluation {
	match := matchEvaluation(lowerMessage, runner.Match)
	if match.Blocked {
		return match
	}
	exampleScore, exampleReason := exampleMatchScore(lowerMessage, runner.Examples)
	if exampleScore > match.Score {
		return routeEvaluation{Score: exampleScore, Reason: exampleReason}
	}
	return match
}

func matchScore(lowerMessage string, matches []string) (int, string) {
	evaluation := matchEvaluation(lowerMessage, matches)
	return evaluation.Score, evaluation.Reason
}

func matchEvaluation(lowerMessage string, matches []string) routeEvaluation {
	bestScore := 0
	bestReason := ""
	for _, match := range matches {
		if match == "*" {
			continue
		}
		negative, term := splitRouteMatch(match)
		if term == "" {
			continue
		}
		if negative && containsRouteTerm(lowerMessage, term) {
			return routeEvaluation{Reason: "blocked: " + term, Blocked: true}
		}
		if negative {
			continue
		}
		if containsRouteTerm(lowerMessage, term) {
			score := len([]rune(term))*10 + strings.Count(lowerMessage, term)*5
			if isExactTokenMatch(lowerMessage, term) {
				score += 20
			}
			if score > bestScore {
				bestScore = score
				bestReason = fmt.Sprintf("matched: %s (score %d)", term, score)
			}
		}
	}
	return routeEvaluation{Score: bestScore, Reason: bestReason}
}

func exampleMatchScore(lowerMessage string, examples []string) (int, string) {
	messageTerms := routeTerms(lowerMessage)
	if len(messageTerms) == 0 {
		return 0, ""
	}
	bestScore := 0
	bestReason := ""
	for _, example := range examples {
		example = strings.TrimSpace(strings.ToLower(example))
		if example == "" {
			continue
		}
		shared := 0
		for term := range routeTerms(example) {
			if _, ok := messageTerms[term]; ok {
				shared++
			}
		}
		if shared == 0 {
			continue
		}
		score := shared*8 + len([]rune(example))/4
		if score > bestScore {
			bestScore = score
			bestReason = fmt.Sprintf("example: %s (score %d)", truncate(example, 80), score)
		}
	}
	return bestScore, bestReason
}

func splitRouteMatch(match string) (bool, string) {
	match = strings.TrimSpace(strings.ToLower(match))
	if strings.HasPrefix(match, "!") {
		return true, strings.TrimSpace(strings.TrimPrefix(match, "!"))
	}
	return false, match
}

func containsRouteTerm(message, term string) bool {
	if term == "" {
		return false
	}
	if !strings.Contains(message, term) {
		return false
	}
	if hasOnlyASCIILettersDigits(term) {
		return isExactTokenMatch(message, term)
	}
	return true
}

func isExactTokenMatch(message, term string) bool {
	start := 0
	for {
		index := strings.Index(message[start:], term)
		if index < 0 {
			return false
		}
		index += start
		beforeOK := index == 0 || !isASCIILetterDigit(rune(message[index-1]))
		afterIndex := index + len(term)
		afterOK := afterIndex >= len(message) || !isASCIILetterDigit(rune(message[afterIndex]))
		if beforeOK && afterOK {
			return true
		}
		start = index + len(term)
	}
}

func hasOnlyASCIILettersDigits(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if !isASCIILetterDigit(r) {
			return false
		}
	}
	return true
}

func isASCIILetterDigit(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func routeTerms(text string) map[string]struct{} {
	terms := map[string]struct{}{}
	for _, field := range strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == ',' || r == '.' || r == '?' || r == '!' || r == ':' || r == ';' || r == '(' || r == ')' || r == '[' || r == ']'
	}) {
		field = strings.TrimSpace(strings.ToLower(field))
		if len([]rune(field)) < 2 {
			continue
		}
		terms[field] = struct{}{}
	}
	return terms
}

func parseAgentCommand(text string) (string, string, bool) {
	fields := strings.Fields(text)
	if len(fields) < 3 {
		return "", "", false
	}
	if normalizeCommand(text) != "/agent" {
		return "", "", false
	}
	name := normalizeAgentName(fields[1])
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), fields[0]))
	message := strings.TrimSpace(strings.TrimPrefix(rest, fields[1]))
	return name, message, name != "" && message != ""
}

func parseMessageCommand(text, command string) (string, bool) {
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return "", false
	}
	if normalizeCommand(text) != command {
		return "", false
	}
	message := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), fields[0]))
	return message, message != ""
}
