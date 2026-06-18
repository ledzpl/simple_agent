package app

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAuthorizeMessageChecksUserAndGroup(t *testing.T) {
	app := NewApp(Config{
		AllowedChatIDs: map[int64]struct{}{123: {}},
		AllowedUserIDs: map[int64]struct{}{7: {}},
	}, nil, &fakeAgent{}, nil)

	if err := app.authorizeMessage(TelegramMessage{
		Chat: TelegramChat{ID: 123, Type: "private"},
		From: &TelegramUser{ID: 7},
	}); err != nil {
		t.Fatalf("authorized private message returned error: %v", err)
	}

	err := app.authorizeMessage(TelegramMessage{
		Chat: TelegramChat{ID: 123, Type: "private"},
		From: &TelegramUser{ID: 8},
	})
	if err == nil || !strings.Contains(err.Error(), "user id: 8") {
		t.Fatalf("expected user allowlist error, got %v", err)
	}

	err = app.authorizeMessage(TelegramMessage{
		Chat: TelegramChat{ID: 123, Type: "group"},
		From: &TelegramUser{ID: 7},
	})
	if err == nil || !strings.Contains(err.Error(), "chat type") {
		t.Fatalf("expected group restriction error, got %v", err)
	}
}

func TestDangerousActionRequiresConfirmation(t *testing.T) {
	app := NewApp(Config{
		AllowedChatIDs: map[int64]struct{}{123: {}},
	}, nil, &fakeAgent{}, nil)

	msg := TelegramMessage{
		MessageID: 10,
		Text:      "please run rm -rf /tmp/example",
		Chat:      TelegramChat{ID: 123, Type: "private"},
		From:      &TelegramUser{ID: 7},
	}
	app.handleMessage(context.Background(), msg)

	app.confirmations.mu.Lock()
	defer app.confirmations.mu.Unlock()
	if len(app.confirmations.pending) != 1 {
		t.Fatalf("expected one pending confirmation, got %d", len(app.confirmations.pending))
	}
	if len(app.jobs.Status(123)) != 0 {
		t.Fatalf("dangerous message should not enqueue before confirmation")
	}
}

func TestRetryDangerousActionRequiresFreshConfirmation(t *testing.T) {
	app := NewApp(Config{
		AllowedChatIDs: map[int64]struct{}{123: {}},
	}, nil, &fakeAgent{}, nil)

	app.jobs.mu.Lock()
	app.jobs.history[123] = []*AgentJob{{
		ID:          "old",
		ChatID:      123,
		UserID:      7,
		ReplyTo:     10,
		Message:     "please run rm -rf /tmp/example",
		ForceDebate: true,
		State:       JobSucceeded,
		CreatedAt:   time.Now().UTC(),
		FinishedAt:  time.Now().UTC(),
	}}
	app.jobs.mu.Unlock()

	app.handleRetry(context.Background(), TelegramMessage{
		MessageID: 20,
		Text:      "/retry last",
		Chat:      TelegramChat{ID: 123, Type: "private"},
		From:      &TelegramUser{ID: 7},
	})

	app.confirmations.mu.Lock()
	if len(app.confirmations.pending) != 1 {
		t.Fatalf("expected one pending retry confirmation, got %d", len(app.confirmations.pending))
	}
	for _, confirmation := range app.confirmations.pending {
		if confirmation.Text != "please run rm -rf /tmp/example" || !confirmation.ForceDebate {
			t.Fatalf("unexpected confirmation: %#v", confirmation)
		}
	}
	app.confirmations.mu.Unlock()

	app.jobs.mu.Lock()
	defer app.jobs.mu.Unlock()
	if len(app.jobs.active[123]) != 0 || len(app.jobs.queued[123]) != 0 {
		t.Fatalf("dangerous retry should not enqueue before confirmation: active=%#v queued=%#v", app.jobs.active[123], app.jobs.queued[123])
	}
}

func TestRedactSecrets(t *testing.T) {
	got := redactSecrets("email watson@example.com token=abc123 phone 010-1234-5678 key sk-abcdefghijklmnopqrstuvwxyz")
	for _, forbidden := range []string{"watson@example.com", "abc123", "010-1234-5678", "sk-abcdefghijklmnopqrstuvwxyz"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("redaction leaked %q in %q", forbidden, got)
		}
	}

	got = redactSecrets("chat id: 1234567890 user id: 1000000000 phone 555-123-4567")
	if !strings.Contains(got, "1234567890") || !strings.Contains(got, "1000000000") {
		t.Fatalf("redaction should preserve bare numeric IDs, got %q", got)
	}
	if strings.Contains(got, "555-123-4567") {
		t.Fatalf("redaction should remove formatted phone numbers, got %q", got)
	}
}
