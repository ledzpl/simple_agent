package app

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type scoredMemory struct {
	index int
	score int
}

func selectRelevantMessages(messages []MemoryMessage, query string, maxMessages, maxChars int) []MemoryMessage {
	if len(messages) == 0 {
		return nil
	}
	queryTerms := memorySearchTerms(query)
	if len(queryTerms) == 0 {
		return limitMessages(messages, maxMessages, maxChars)
	}

	scored := make([]scoredMemory, 0, len(messages))
	relevant := false
	for i, message := range messages {
		score := memoryRelevanceScore(message.Content, queryTerms)
		if score > 0 {
			relevant = true
		}
		scored = append(scored, scoredMemory{index: i, score: score})
	}
	if !relevant {
		return limitMessages(messages, maxMessages, maxChars)
	}

	recentFallback := 3
	if maxMessages > 0 && recentFallback > maxMessages {
		recentFallback = maxMessages
	}
	for i := len(scored) - recentFallback; i < len(scored); i++ {
		if i >= 0 {
			scored[i].score += 5
		}
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].index > scored[j].index
	})

	selected := make([]scoredMemory, 0, len(scored))
	totalChars := 0
	for _, candidate := range scored {
		if candidate.score <= 0 {
			continue
		}
		if maxMessages > 0 && len(selected) >= maxMessages {
			break
		}
		chars := len([]rune(messages[candidate.index].Content))
		if maxChars > 0 && totalChars+chars > maxChars {
			if len(selected) == 0 {
				selected = append(selected, candidate)
				totalChars = maxChars
				break
			}
			continue
		}
		selected = append(selected, candidate)
		totalChars += chars
	}
	if len(selected) == 0 {
		return nil
	}
	sort.Slice(selected, func(i, j int) bool {
		return selected[i].index < selected[j].index
	})

	out := make([]MemoryMessage, 0, len(selected))
	for _, candidate := range selected {
		message := messages[candidate.index]
		if maxChars > 0 && len([]rune(message.Content)) > maxChars {
			message.Content = truncateMemoryContent(message.Content, maxChars)
		}
		out = append(out, message)
	}
	return out
}

func memoryRelevanceScore(content string, queryTerms map[string]struct{}) int {
	content = strings.ToLower(content)
	contentTerms := memorySearchTerms(content)
	score := 0
	for term := range queryTerms {
		if _, ok := contentTerms[term]; ok {
			score += 30 + len([]rune(term))
			continue
		}
		if strings.Contains(content, term) {
			score += 12 + len([]rune(term))
		}
	}
	return score
}

func memorySearchTerms(text string) map[string]struct{} {
	stop := map[string]struct{}{
		"그거": {}, "그리고": {}, "대한": {}, "관련": {}, "무엇": {}, "어떻게": {},
		"이거": {}, "이번": {}, "저거": {}, "정리": {}, "질문": {}, "해줘": {},
		"about": {}, "and": {}, "for": {}, "how": {}, "please": {}, "that": {},
		"the": {}, "this": {}, "what": {}, "with": {},
	}
	terms := map[string]struct{}{}
	for _, field := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= '0' && r <= '9') &&
			!(r >= 'a' && r <= 'z') &&
			!(r >= 'A' && r <= 'Z') &&
			!(r >= '가' && r <= '힣')
	}) {
		field = strings.TrimSpace(field)
		if len([]rune(field)) < 2 {
			continue
		}
		if _, blocked := stop[field]; blocked {
			continue
		}
		terms[field] = struct{}{}
	}
	return terms
}

func promptWithMemory(memoryContext, userPrompt string) string {
	userPrompt = strings.TrimSpace(userPrompt)
	memoryContext = strings.TrimSpace(memoryContext)
	var out strings.Builder
	out.WriteString(`Response protocol:
- Identify the user's actual objective and answer it directly.
- Distinguish verified facts, assumptions, and uncertainty. Never invent facts, sources, tool results, or current information.
- Check for contradictions, missing constraints, safety issues, and likely failure modes before answering.
- Prefer concrete recommendations, examples, commands, or next steps when useful.
- Ask a clarifying question only when the missing information would materially change the answer; otherwise state a reasonable assumption.
- Do not reveal private chain-of-thought. Provide concise conclusions and the evidence or reasoning needed to evaluate them.`)
	if memoryContext != "" {
		out.WriteString("\n\n")
		out.WriteString(memoryContext)
	}
	out.WriteString("\n\nCurrent Telegram user message:\n")
	out.WriteString(userPrompt)
	return out.String()
}

func buildMemoryRefinePrompt(userMessage, assistantMessage string, maxChars int) string {
	return fmt.Sprintf(`Summarize this Telegram exchange into durable memory for future replies.

Rules:
- Keep only stable facts, user preferences, decisions, constraints, names, IDs, task state, or promised follow-ups.
- Remove filler, greetings, repeated context, transient wording, and full code/output unless the exact detail is important.
- Use the user's language when possible.
- Return NO_MEMORY if there is nothing worth remembering.
- Keep the result within %d characters.
- Return only the memory text, with no heading.

User message:
%s

Assistant response:
%s`, maxChars, strings.TrimSpace(userMessage), strings.TrimSpace(assistantMessage))
}

func normalizeMemoryNote(note string, maxChars int) string {
	note = strings.TrimSpace(note)
	note = strings.Trim(note, "`")
	note = strings.TrimSpace(note)
	if strings.EqualFold(note, "NO_MEMORY") || note == "" {
		return ""
	}
	if maxChars <= 0 {
		return note
	}
	runes := []rune(note)
	if len(runes) <= maxChars {
		return note
	}
	return strings.TrimSpace(string(runes[:maxChars]))
}

func compactMemoryNote(userMessage, assistantMessage string, maxChars int) string {
	note := strings.TrimSpace(fmt.Sprintf("User asked: %s\nAssistant answered: %s", compactLine(userMessage), compactLine(assistantMessage)))
	return normalizeMemoryNote(note, maxChars)
}

func compactLine(text string) string {
	text = strings.TrimSpace(text)
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= 300 {
		return text
	}
	return string(runes[:300]) + "..."
}

var (
	memoryEmailPattern         = regexp.MustCompile(`(?i)\b[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}\b`)
	memoryKoreanPhonePattern   = regexp.MustCompile(`\b01[016789][-\s]?\d{3,4}[-\s]?\d{4}\b`)
	memoryUSPhonePattern       = regexp.MustCompile(`\b(?:\+1[-.\s]?)?(?:\(?\d{3}\)?[-.\s]?)\d{3}[-.\s]?\d{4}\b`)
	memoryTelegramTokenPattern = regexp.MustCompile(`\d{6,}:[A-Za-z0-9_-]{20,}`)
	memoryOpenAIKeyPattern     = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`)
)

func redactMemoryContent(text string) string {
	text = memoryEmailPattern.ReplaceAllString(text, "[redacted-email]")
	text = memoryKoreanPhonePattern.ReplaceAllString(text, "[redacted-phone]")
	text = memoryUSPhonePattern.ReplaceAllString(text, "[redacted-phone]")
	text = memoryTelegramTokenPattern.ReplaceAllString(text, "[redacted-token]")
	text = memoryOpenAIKeyPattern.ReplaceAllString(text, "[redacted-token]")
	return redactSecrets(text)
}

func newMemoryID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}
