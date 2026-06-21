package app

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

const (
	telegramMessageLimit      = 4096
	telegramResponseBodyLimit = 8 << 20
)

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
	UpdateID      int64                  `json:"update_id"`
	Message       *TelegramMessage       `json:"message"`
	CallbackQuery *TelegramCallbackQuery `json:"callback_query"`
}

type TelegramMessage struct {
	MessageID      int64            `json:"message_id"`
	Date           int64            `json:"date"`
	Text           string           `json:"text"`
	Chat           TelegramChat     `json:"chat"`
	From           *TelegramUser    `json:"from"`
	ReplyToMessage *TelegramMessage `json:"reply_to_message"`
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

type TelegramCallbackQuery struct {
	ID      string           `json:"id"`
	From    *TelegramUser    `json:"from"`
	Message *TelegramMessage `json:"message"`
	Data    string           `json:"data"`
}

type SendMessageOptions struct {
	ReplyMarkup *InlineKeyboardMarkup
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
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

type sendMessageResponse struct {
	OK          bool            `json:"ok"`
	Result      TelegramMessage `json:"result"`
	Description string          `json:"description"`
}

func (b *TelegramBot) GetUpdates(ctx context.Context, offset int64) ([]TelegramUpdate, error) {
	values := url.Values{}
	values.Set("timeout", "50")
	values.Set("allowed_updates", `["message","callback_query"]`)
	if offset > 0 {
		values.Set("offset", strconv.FormatInt(offset, 10))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/getUpdates?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, telegramRequestError("getUpdates", err)
	}
	defer resp.Body.Close()

	body, err := readLimitedBody(resp.Body, telegramResponseBodyLimit)
	if err != nil {
		return nil, fmt.Errorf("read telegram getUpdates response: %w", err)
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
	_, err := b.SendMessageWithOptions(ctx, chatID, text, replyTo, SendMessageOptions{})
	return err
}

func (b *TelegramBot) SendMessageWithOptions(ctx context.Context, chatID int64, text string, replyTo int64, options SendMessageOptions) ([]TelegramMessage, error) {
	chunks := splitTelegramText(text)
	sent := make([]TelegramMessage, 0, len(chunks))
	for i, chunk := range chunks {
		payload := map[string]any{
			"chat_id": chatID,
			"text":    chunk,
		}
		if replyTo > 0 {
			payload["reply_parameters"] = map[string]any{"message_id": replyTo}
		}
		if options.ReplyMarkup != nil && i == len(chunks)-1 {
			payload["reply_markup"] = options.ReplyMarkup
		}
		message, err := b.sendMessagePayload(ctx, payload)
		if err != nil {
			return sent, err
		}
		sent = append(sent, message)
		replyTo = 0
	}
	return sent, nil
}

func (b *TelegramBot) sendMessagePayload(ctx context.Context, payload map[string]any) (TelegramMessage, error) {
	var decoded sendMessageResponse
	if err := b.postJSONDecode(ctx, "/sendMessage", payload, &decoded); err != nil {
		return TelegramMessage{}, err
	}
	if !decoded.OK {
		return TelegramMessage{}, fmt.Errorf("telegram sendMessage failed: %s", decoded.Description)
	}
	return decoded.Result, nil
}

func (b *TelegramBot) SendChatAction(ctx context.Context, chatID int64, action string) error {
	return b.postJSON(ctx, "/sendChatAction", map[string]any{
		"chat_id": chatID,
		"action":  action,
	})
}

func (b *TelegramBot) EditMessageText(ctx context.Context, chatID, messageID int64, text string, options SendMessageOptions) error {
	payload := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       strings.TrimSpace(text),
	}
	if payload["text"] == "" {
		payload["text"] = "(empty response)"
	}
	if options.ReplyMarkup != nil {
		payload["reply_markup"] = options.ReplyMarkup
	}
	return b.postJSON(ctx, "/editMessageText", payload)
}

func (b *TelegramBot) AnswerCallbackQuery(ctx context.Context, callbackID, text string) error {
	payload := map[string]any{
		"callback_query_id": callbackID,
	}
	if strings.TrimSpace(text) != "" {
		payload["text"] = text
	}
	return b.postJSON(ctx, "/answerCallbackQuery", payload)
}

func (b *TelegramBot) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	return b.postJSON(ctx, "/deleteMessage", map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
	})
}

func (b *TelegramBot) postJSON(ctx context.Context, path string, payload map[string]any) error {
	var decoded apiResponse
	if err := b.postJSONDecode(ctx, path, payload, &decoded); err != nil {
		return err
	}
	if !decoded.OK {
		return fmt.Errorf("telegram %s failed: %s", strings.TrimPrefix(path, "/"), decoded.Description)
	}
	return nil
}

func (b *TelegramBot) postJSONDecode(ctx context.Context, path string, payload map[string]any, out any) error {
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
		return telegramRequestError(strings.TrimPrefix(path, "/"), err)
	}
	defer resp.Body.Close()

	body, err := readLimitedBody(resp.Body, telegramResponseBodyLimit)
	if err != nil {
		return fmt.Errorf("read telegram %s response: %w", strings.TrimPrefix(path, "/"), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram %s status %d: %s", strings.TrimPrefix(path, "/"), resp.StatusCode, trimBody(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode telegram %s: %w", strings.TrimPrefix(path, "/"), err)
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
	return truncate(text, 1000)
}

func readLimitedBody(body io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("response body limit must be positive")
	}
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response body exceeds %d bytes", limit)
	}
	return data, nil
}

func telegramRequestError(operation string, err error) error {
	return fmt.Errorf("telegram %s request failed: %s", operation, redactSecrets(err.Error()))
}
