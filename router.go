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
}

type AgentParticipant struct {
	Runner AgentRunner
	Score  int
	Reason string
	index  int
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
	for i, runner := range r.runners {
		score, reason := matchScore(lower, runner.Match)
		if score > bestScore {
			bestScore = score
			bestIndex = i
			bestReason = reason
		}
	}

	return AgentRoute{
		Runner:  r.runners[bestIndex],
		Message: message,
		Reason:  bestReason,
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
		score, reason := matchScore(lower, runner.Match)
		candidates = append(candidates, AgentParticipant{
			Runner: runner,
			Score:  score,
			Reason: reason,
			index:  i,
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
		if candidate.Score <= 0 {
			continue
		}
		selected = append(selected, candidate)
		used[candidate.Runner.Name] = struct{}{}
		if len(selected) == maxAgents {
			return selected
		}
	}

	if len(selected) == 0 {
		return []AgentParticipant{{
			Runner: r.runners[r.defaultIndex],
			Reason: "default participant",
			index:  r.defaultIndex,
		}}
	}

	if _, ok := used[r.runners[r.defaultIndex].Name]; !ok && len(selected) < maxAgents {
		selected = append(selected, AgentParticipant{
			Runner: r.runners[r.defaultIndex],
			Reason: "default participant",
			index:  r.defaultIndex,
		})
	}

	return selected
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

func matchScore(lowerMessage string, matches []string) (int, string) {
	bestScore := 0
	bestReason := ""
	for _, match := range matches {
		if match == "*" {
			continue
		}
		if strings.Contains(lowerMessage, match) {
			score := len([]rune(match))
			if score > bestScore {
				bestScore = score
				bestReason = "matched: " + match
			}
		}
	}
	return bestScore, bestReason
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
