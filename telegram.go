package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const telegramMessageLimit = 4096

type TelegramBot struct {
	baseURL string
	client  *http.Client
}

func NewTelegramBot(token string) *TelegramBot {
	return &TelegramBot{
		baseURL: "https://api.telegram.org/bot" + token,
		client: &http.Client{
			Timeout: 65 * time.Second,
		},
	}
}

type TelegramUpdate struct {
	UpdateID int64            `json:"update_id"`
	Message  *TelegramMessage `json:"message"`
}

type TelegramMessage struct {
	MessageID int64         `json:"message_id"`
	Date      int64         `json:"date"`
	Text      string        `json:"text"`
	Chat      TelegramChat  `json:"chat"`
	From      *TelegramUser `json:"from"`
}

type TelegramChat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

type TelegramUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type getUpdatesResponse struct {
	OK          bool             `json:"ok"`
	Result      []TelegramUpdate `json:"result"`
	Description string           `json:"description"`
}

type apiResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

func (b *TelegramBot) GetUpdates(ctx context.Context, offset int64) ([]TelegramUpdate, error) {
	values := url.Values{}
	values.Set("timeout", "50")
	values.Set("allowed_updates", `["message"]`)
	if offset > 0 {
		values.Set("offset", strconv.FormatInt(offset, 10))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/getUpdates?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("telegram getUpdates status %d: %s", resp.StatusCode, trimBody(body))
	}

	var decoded getUpdatesResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode getUpdates: %w", err)
	}
	if !decoded.OK {
		return nil, fmt.Errorf("telegram getUpdates failed: %s", decoded.Description)
	}
	return decoded.Result, nil
}

func (b *TelegramBot) SendMessage(ctx context.Context, chatID int64, text string, replyTo int64) error {
	for _, chunk := range splitTelegramText(text) {
		payload := map[string]any{
			"chat_id": chatID,
			"text":    chunk,
		}
		if replyTo > 0 {
			payload["reply_parameters"] = map[string]any{"message_id": replyTo}
		}
		if err := b.postJSON(ctx, "/sendMessage", payload); err != nil {
			return err
		}
		replyTo = 0
	}
	return nil
}

func (b *TelegramBot) SendChatAction(ctx context.Context, chatID int64, action string) error {
	return b.postJSON(ctx, "/sendChatAction", map[string]any{
		"chat_id": chatID,
		"action":  action,
	})
}

func (b *TelegramBot) postJSON(ctx context.Context, path string, payload map[string]any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram %s status %d: %s", strings.TrimPrefix(path, "/"), resp.StatusCode, trimBody(body))
	}

	var decoded apiResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return fmt.Errorf("decode telegram %s: %w", strings.TrimPrefix(path, "/"), err)
	}
	if !decoded.OK {
		return fmt.Errorf("telegram %s failed: %s", strings.TrimPrefix(path, "/"), decoded.Description)
	}
	return nil
}

func splitTelegramText(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{"(empty response)"}
	}

	runes := []rune(text)
	var chunks []string
	for len(runes) > telegramMessageLimit {
		cut := telegramMessageLimit
		for i := telegramMessageLimit; i > telegramMessageLimit-500 && i > 0; i-- {
			if runes[i-1] == '\n' {
				cut = i
				break
			}
		}
		chunks = append(chunks, strings.TrimSpace(string(runes[:cut])))
		runes = runes[cut:]
	}
	chunks = append(chunks, strings.TrimSpace(string(runes)))
	return chunks
}

func trimBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 1000 {
		return text[:1000] + "..."
	}
	return text
}
