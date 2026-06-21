package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestMemoryStoreAppendBuildContextAndClear(t *testing.T) {
	store, err := NewMemoryStore(Config{
		MemoryEnabled: true,
		MemoryDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewMemoryStore returned error: %v", err)
	}

	ctx := context.Background()
	if err := store.AppendNote(ctx, 123, "사용자의 이름은 Watson이다."); err != nil {
		t.Fatalf("AppendNote returned error: %v", err)
	}

	memoryContext, err := store.BuildContext(ctx, 123)
	if err != nil {
		t.Fatalf("BuildContext returned error: %v", err)
	}
	for _, want := range []string{"Relevant memory from previous Telegram conversations", "Memory: 사용자의 이름은 Watson이다."} {
		if !strings.Contains(memoryContext, want) {
			t.Fatalf("memory context missing %q:\n%s", want, memoryContext)
		}
	}

	stats, err := store.Stats(ctx, 123)
	if err != nil {
		t.Fatalf("Stats returned error: %v", err)
	}
	if stats.Messages != 1 || stats.Bytes == 0 {
		t.Fatalf("unexpected stats: %#v", stats)
	}

	if err := store.Clear(123); err != nil {
		t.Fatalf("Clear returned error: %v", err)
	}
	stats, err = store.Stats(ctx, 123)
	if err != nil {
		t.Fatalf("Stats after clear returned error: %v", err)
	}
	if stats.Messages != 0 || stats.Bytes != 0 {
		t.Fatalf("memory was not cleared: %#v", stats)
	}
}

func TestLimitMessages(t *testing.T) {
	if limited := limitMessages(nil, 10, 10); limited != nil {
		t.Fatalf("empty memory should remain empty, got %#v", limited)
	}

	messages := []MemoryMessage{
		{Role: RoleUser, Content: "one"},
		{Role: RoleAssistant, Content: "two"},
		{Role: RoleUser, Content: "three"},
		{Role: RoleAssistant, Content: "four"},
	}

	limited := limitMessages(messages, 3, 1000)
	if len(limited) != 3 || limited[0].Content != "two" {
		t.Fatalf("max messages limit failed: %#v", limited)
	}

	limited = limitMessages(messages, 10, 9)
	if len(limited) != 2 || limited[0].Content != "three" || limited[1].Content != "four" {
		t.Fatalf("max chars limit failed: %#v", limited)
	}

	limited = limitMessages([]MemoryMessage{{Role: RoleAssistant, Content: "0123456789"}}, 10, 4)
	if len(limited) != 1 || limited[0].Content != "0123" {
		t.Fatalf("oversized latest memory should be truncated, got %#v", limited)
	}
}

func TestPromptWithMemory(t *testing.T) {
	got := promptWithMemory("User: old", "new")
	for _, want := range []string{"Response protocol:", "Distinguish verified facts", "User: old", "Current Telegram user message:\nnew"} {
		if !strings.Contains(got, want) {
			t.Fatalf("promptWithMemory missing %q:\n%s", want, got)
		}
	}
	got = promptWithMemory("", "new")
	if !strings.Contains(got, "Response protocol:") || !strings.HasSuffix(got, "Current Telegram user message:\nnew") {
		t.Fatalf("promptWithMemory mismatch:\n%s", got)
	}
}

func TestSelectRelevantMessagesPrefersQueryMatchesAndRecentFallback(t *testing.T) {
	messages := []MemoryMessage{
		{Role: RoleMemory, Content: "사용자는 일본 여행에서 조용한 숙소를 선호한다."},
		{Role: RoleMemory, Content: "사용자는 Go 코드에서 테이블 테스트를 선호한다."},
		{Role: RoleMemory, Content: "점심 메뉴는 비빔밥이었다."},
		{Role: RoleMemory, Content: "최근 프로젝트 배포가 완료되었다."},
		{Role: RoleMemory, Content: "다음 주 회의는 화요일이다."},
		{Role: RoleMemory, Content: "사용자는 답변을 한국어로 원한다."},
	}

	selected := selectRelevantMessages(messages, "일본 여행 숙소를 추천해줘", 4, 1000)
	if len(selected) != 4 {
		t.Fatalf("expected one relevant memory plus three recent fallbacks, got %#v", selected)
	}
	if selected[0].Content != messages[0].Content {
		t.Fatalf("older relevant memory should be retained, got %#v", selected)
	}
	for _, message := range selected {
		if message.Content == messages[1].Content || message.Content == messages[2].Content {
			t.Fatalf("irrelevant older memory should be excluded, got %#v", selected)
		}
	}

	selected = selectRelevantMessages([]MemoryMessage{
		{Role: RoleMemory, Content: "Go " + strings.Repeat("x", 100)},
	}, "Go", 4, 10)
	if len(selected) != 1 || len([]rune(selected[0].Content)) != 10 {
		t.Fatalf("oversized relevant memory should be truncated, got %#v", selected)
	}
}

func TestBuildContextForQueryMarksMemoryAsUntrusted(t *testing.T) {
	store, err := NewMemoryStore(Config{
		MemoryEnabled: true,
		MemoryDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewMemoryStore returned error: %v", err)
	}
	if err := store.AppendNote(context.Background(), 123, "사용자는 Go 테스트를 선호한다."); err != nil {
		t.Fatalf("AppendNote returned error: %v", err)
	}

	contextText, err := store.BuildContextForQuery(context.Background(), 123, "Go 테스트 작성")
	if err != nil {
		t.Fatalf("BuildContextForQuery returned error: %v", err)
	}
	for _, want := range []string{"Relevant memory", "untrusted context", "Go 테스트"} {
		if !strings.Contains(contextText, want) {
			t.Fatalf("query context missing %q:\n%s", want, contextText)
		}
	}
}

func TestMemoryRefinePromptAndNormalize(t *testing.T) {
	prompt := buildMemoryRefinePrompt("내 이름은 Watson이야", "앞으로 Watson이라고 부르겠습니다.", 120)
	for _, want := range []string{"durable memory", "NO_MEMORY", "내 이름은 Watson이야", "앞으로 Watson이라고 부르겠습니다."} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}

	if got := normalizeMemoryNote("```사용자의 이름은 Watson이다.```", 100); got != "사용자의 이름은 Watson이다." {
		t.Fatalf("normalize markdown fence mismatch: %q", got)
	}
	if got := normalizeMemoryNote("NO_MEMORY", 100); got != "" {
		t.Fatalf("NO_MEMORY should be empty: %q", got)
	}
	if got := normalizeMemoryNote("가나다라마", 3); got != "가나다" {
		t.Fatalf("max char truncation mismatch: %q", got)
	}
}

func TestMemoryLoadSkipsCorruptLinesAndRepair(t *testing.T) {
	store, err := NewMemoryStore(Config{
		MemoryEnabled: true,
		MemoryDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewMemoryStore returned error: %v", err)
	}

	data := `{"role":"memory","content":"valid","created_at":"2026-06-17T00:00:00Z"}
not json
{"role":"memory","content":"also valid","created_at":"2026-06-17T00:00:01Z"}
`
	if err := os.WriteFile(store.path(123), []byte(data), 0600); err != nil {
		t.Fatalf("write memory file: %v", err)
	}

	messages, issues, err := store.LoadDetailed(context.Background(), 123)
	if err != nil {
		t.Fatalf("LoadDetailed returned error: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 valid messages, got %#v", messages)
	}
	if len(issues) != 1 || issues[0].Line != 2 {
		t.Fatalf("expected one issue on line 2, got %#v", issues)
	}

	stats, err := store.Repair(context.Background(), 123)
	if err != nil {
		t.Fatalf("Repair returned error: %v", err)
	}
	if stats.InvalidLines != 1 || stats.Messages != 2 {
		t.Fatalf("unexpected repair stats: %#v", stats)
	}
	_, issues, err = store.LoadDetailed(context.Background(), 123)
	if err != nil {
		t.Fatalf("LoadDetailed after repair returned error: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("repair should remove invalid lines, got %#v", issues)
	}
}

func TestMemoryDeleteExportAndRedact(t *testing.T) {
	store, err := NewMemoryStore(Config{
		MemoryEnabled: true,
		MemoryDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewMemoryStore returned error: %v", err)
	}

	ctx := context.Background()
	if err := store.AppendNote(ctx, 123, "email watson@example.com phone 010-1234-5678 token 123456:abcdefghijklmnopqrstuvwxyz"); err != nil {
		t.Fatalf("AppendNote returned error: %v", err)
	}
	if err := store.AppendNote(ctx, 123, "keep this"); err != nil {
		t.Fatalf("AppendNote returned error: %v", err)
	}
	if err := store.Delete(123, 2); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}

	exported, issues, err := store.ExportJSONL(ctx, 123)
	if err != nil {
		t.Fatalf("ExportJSONL returned error: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("unexpected export issues: %#v", issues)
	}
	for _, forbidden := range []string{"watson@example.com", "010-1234-5678", "123456:abcdefghijklmnopqrstuvwxyz", "keep this"} {
		if strings.Contains(exported, forbidden) {
			t.Fatalf("export leaked or retained %q:\n%s", forbidden, exported)
		}
	}
	for _, want := range []string{"[redacted-email]", "[redacted-phone]", "[redacted-token]"} {
		if !strings.Contains(exported, want) {
			t.Fatalf("export missing redaction %q:\n%s", want, exported)
		}
	}
}

func TestMemoryDeleteByID(t *testing.T) {
	store, err := NewMemoryStore(Config{
		MemoryEnabled: true,
		MemoryDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewMemoryStore returned error: %v", err)
	}

	ctx := context.Background()
	if _, err := store.AppendNoteWithID(ctx, 123, "answer-a", "first answer memory"); err != nil {
		t.Fatalf("AppendNoteWithID returned error: %v", err)
	}
	if _, err := store.AppendNoteWithID(ctx, 123, "answer-b", "second answer memory"); err != nil {
		t.Fatalf("AppendNoteWithID returned error: %v", err)
	}

	if err := store.DeleteByID(123, "answer-a"); err != nil {
		t.Fatalf("DeleteByID returned error: %v", err)
	}
	messages, err := store.Load(ctx, 123)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(messages) != 1 || messages[0].ID != "answer-b" || messages[0].Content != "second answer memory" {
		t.Fatalf("DeleteByID removed the wrong memory: %#v", messages)
	}
}

func TestMemoryExchangeEntriesShareIDAndDeleteTogether(t *testing.T) {
	store, err := NewMemoryStore(Config{
		MemoryEnabled: true,
		MemoryDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewMemoryStore returned error: %v", err)
	}

	ctx := context.Background()
	id, err := store.AppendExchangeWithID(ctx, 123, "exchange-a", "D", "정답은 A. 텅스텐입니다.")
	if err != nil {
		t.Fatalf("AppendExchangeWithID returned error: %v", err)
	}
	if id != "exchange-a" {
		t.Fatalf("exchange id = %q", id)
	}
	if _, err := store.AppendNoteWithID(ctx, 123, id, "퀴즈 1번 완료"); err != nil {
		t.Fatalf("AppendNoteWithID returned error: %v", err)
	}

	messages, err := store.Load(ctx, 123)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("expected three exchange records, got %#v", messages)
	}
	for _, message := range messages {
		if message.ID != id {
			t.Fatalf("exchange id mismatch: %#v", messages)
		}
	}

	if err := store.DeleteByID(123, id); err != nil {
		t.Fatalf("DeleteByID returned error: %v", err)
	}
	messages, err = store.Load(ctx, 123)
	if err != nil {
		t.Fatalf("Load after delete returned error: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("DeleteByID should remove the full exchange: %#v", messages)
	}
}

func TestMemoryDeleteByIndexRemovesFullExchange(t *testing.T) {
	store, err := NewMemoryStore(Config{
		MemoryEnabled: true,
		MemoryDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewMemoryStore returned error: %v", err)
	}
	ctx := context.Background()
	if _, err := store.AppendExchangeWithID(ctx, 123, "exchange-a", "D", "정답은 A"); err != nil {
		t.Fatalf("AppendExchangeWithID returned error: %v", err)
	}
	if _, err := store.AppendNoteWithID(ctx, 123, "exchange-a", "퀴즈 상태"); err != nil {
		t.Fatalf("AppendNoteWithID returned error: %v", err)
	}

	if err := store.Delete(123, 2); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	messages, err := store.Load(ctx, 123)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("index deletion should remove the full exchange: %#v", messages)
	}
}

func TestMemoryStoreConcurrentAppendsDoNotLoseEntries(t *testing.T) {
	store, err := NewMemoryStore(Config{
		MemoryEnabled: true,
		MemoryDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewMemoryStore returned error: %v", err)
	}

	const count = 50
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := store.AppendNote(context.Background(), 123, fmt.Sprintf("note-%d", i)); err != nil {
				t.Errorf("AppendNote(%d): %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	messages, err := store.Load(context.Background(), 123)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(messages) != count {
		t.Fatalf("stored messages = %d, want %d", len(messages), count)
	}
}
