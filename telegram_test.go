package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSplitTelegramText(t *testing.T) {
	long := make([]rune, telegramMessageLimit+20)
	for i := range long {
		long[i] = '가'
	}

	chunks := splitTelegramText(string(long))
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	for i, chunk := range chunks {
		if got := len([]rune(chunk)); got > telegramMessageLimit {
			t.Fatalf("chunk %d has %d runes, limit %d", i, got, telegramMessageLimit)
		}
	}
}

func TestTelegramBotGetUpdates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/getUpdates" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("offset"); got != "42" {
			t.Fatalf("offset mismatch: %s", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"ok": true,
			"result": [{
				"update_id": 42,
				"message": {
					"message_id": 7,
					"text": "hello",
					"chat": {"id": 123, "type": "private"}
				}
			}]
		}`))
	}))
	defer server.Close()

	bot := &TelegramBot{baseURL: server.URL, client: server.Client()}
	updates, err := bot.GetUpdates(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetUpdates returned error: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}
	if updates[0].Message == nil || updates[0].Message.Text != "hello" || updates[0].Message.Chat.ID != 123 {
		t.Fatalf("unexpected update: %#v", updates[0])
	}
}

func TestTelegramBotSendMessage(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sendMessage" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			t.Fatalf("unexpected content type: %s", r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer server.Close()

	bot := &TelegramBot{baseURL: server.URL, client: server.Client()}
	if err := bot.SendMessage(context.Background(), 123, "answer", 7); err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}

	if got := payload["chat_id"]; got != float64(123) {
		t.Fatalf("chat_id mismatch: %#v", got)
	}
	if got := payload["text"]; got != "answer" {
		t.Fatalf("text mismatch: %#v", got)
	}
	reply, ok := payload["reply_parameters"].(map[string]any)
	if !ok {
		t.Fatalf("missing reply_parameters: %#v", payload["reply_parameters"])
	}
	if got := reply["message_id"]; got != float64(7) {
		t.Fatalf("reply message id mismatch: %#v", got)
	}
}

func TestTelegramBotSendMessageWithOptions(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sendMessage" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok": true, "result": {"message_id": 99, "text": "*answer*", "chat": {"id": 123}}}`))
	}))
	defer server.Close()

	bot := &TelegramBot{baseURL: server.URL, client: server.Client()}
	sent, err := bot.SendMessageWithOptions(context.Background(), 123, "*answer*", 7, SendMessageOptions{
		ReplyMarkup: &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{
			{Text: "다시 생성", CallbackData: "regenerate"},
		}}},
	})
	if err != nil {
		t.Fatalf("SendMessageWithOptions returned error: %v", err)
	}
	if len(sent) != 1 || sent[0].MessageID != 99 {
		t.Fatalf("sent messages mismatch: %#v", sent)
	}
	if _, ok := payload["parse_mode"]; ok {
		t.Fatalf("sendMessage should use plain text: %#v", payload)
	}
	markup, ok := payload["reply_markup"].(map[string]any)
	if !ok {
		t.Fatalf("missing reply_markup: %#v", payload["reply_markup"])
	}
	keyboard, ok := markup["inline_keyboard"].([]any)
	if !ok || len(keyboard) != 1 {
		t.Fatalf("inline_keyboard mismatch: %#v", markup["inline_keyboard"])
	}
}

func TestTelegramBotEditMessageText(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/editMessageText" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer server.Close()

	bot := &TelegramBot{baseURL: server.URL, client: server.Client()}
	err := bot.EditMessageText(context.Background(), 123, 99, "작업 진행 중입니다.", SendMessageOptions{
		ReplyMarkup: &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{
			{Text: "취소", CallbackData: "job_cancel:j1"},
		}}},
	})
	if err != nil {
		t.Fatalf("EditMessageText returned error: %v", err)
	}
	if got := payload["chat_id"]; got != float64(123) {
		t.Fatalf("chat_id mismatch: %#v", got)
	}
	if got := payload["message_id"]; got != float64(99) {
		t.Fatalf("message_id mismatch: %#v", got)
	}
	if got := payload["text"]; got != "작업 진행 중입니다." {
		t.Fatalf("text mismatch: %#v", got)
	}
	if _, ok := payload["parse_mode"]; ok {
		t.Fatalf("editMessageText should use plain text: %#v", payload)
	}
	if _, ok := payload["reply_markup"].(map[string]any); !ok {
		t.Fatalf("missing reply_markup: %#v", payload["reply_markup"])
	}
}
