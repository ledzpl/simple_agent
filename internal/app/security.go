package app

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type PendingConfirmation struct {
	ID          string
	Message     TelegramMessage
	Text        string
	ForceDebate bool
	CreatedAt   time.Time
}

type ConfirmationStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	pending map[string]PendingConfirmation
}

func NewConfirmationStore(ttl time.Duration) *ConfirmationStore {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &ConfirmationStore{ttl: ttl, pending: map[string]PendingConfirmation{}}
}

func (s *ConfirmationStore) Add(msg TelegramMessage, text string, forceDebate bool) PendingConfirmation {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(time.Now())
	confirmation := PendingConfirmation{
		ID:          newConfirmationID(),
		Message:     msg,
		Text:        text,
		ForceDebate: forceDebate,
		CreatedAt:   time.Now().UTC(),
	}
	s.pending[confirmation.ID] = confirmation
	return confirmation
}

func (s *ConfirmationStore) Take(id string, chatID, userID int64) (PendingConfirmation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.cleanupLocked(now)
	confirmation, ok := s.pending[id]
	if !ok {
		return PendingConfirmation{}, false
	}
	if confirmation.Message.Chat.ID != chatID {
		return PendingConfirmation{}, false
	}
	if confirmation.Message.From != nil && confirmation.Message.From.ID != userID {
		return PendingConfirmation{}, false
	}
	delete(s.pending, id)
	return confirmation, true
}

func (s *ConfirmationStore) cleanupLocked(now time.Time) {
	for id, confirmation := range s.pending {
		if now.Sub(confirmation.CreatedAt) > s.ttl {
			delete(s.pending, id)
		}
	}
}

func newConfirmationID() string {
	return "c" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func (a *App) authorizeMessage(msg TelegramMessage) error {
	if !a.isChatTypeAllowed(msg.Chat) {
		return fmt.Errorf("이 chat type은 허용되지 않습니다: %s", msg.Chat.Type)
	}
	if !a.isChatAllowed(msg.Chat.ID) {
		return fmt.Errorf("이 채팅은 허용 목록에 없습니다.\nchat id: %d\n\n서버의 TELEGRAM_ALLOWED_CHAT_IDS에 이 값을 추가한 뒤 다시 실행하세요.", msg.Chat.ID)
	}
	if !a.isUserAllowed(msg.From) {
		userID := int64(0)
		if msg.From != nil {
			userID = msg.From.ID
		}
		return fmt.Errorf("이 사용자는 허용 목록에 없습니다.\nuser id: %d\n\n서버의 TELEGRAM_ALLOWED_USER_IDS에 이 값을 추가하세요.", userID)
	}
	return nil
}

func (a *App) authorizeCallback(query TelegramCallbackQuery) error {
	if query.Message == nil {
		return fmt.Errorf("처리할 메시지를 찾지 못했습니다.")
	}
	if !a.isChatTypeAllowed(query.Message.Chat) {
		return fmt.Errorf("이 chat type은 허용되지 않습니다: %s", query.Message.Chat.Type)
	}
	if !a.isChatAllowed(query.Message.Chat.ID) {
		return fmt.Errorf("허용되지 않은 chat입니다.")
	}
	if !a.isUserAllowed(query.From) {
		return fmt.Errorf("허용되지 않은 사용자입니다.")
	}
	return nil
}

func (a *App) isChatAllowed(chatID int64) bool {
	_, ok := a.cfg.AllowedChatIDs[chatID]
	return ok
}

func (a *App) isUserAllowed(user *TelegramUser) bool {
	if len(a.cfg.AllowedUserIDs) == 0 {
		return true
	}
	if user == nil {
		return false
	}
	_, ok := a.cfg.AllowedUserIDs[user.ID]
	return ok
}

func (a *App) isChatTypeAllowed(chat TelegramChat) bool {
	switch chat.Type {
	case "", "private":
		return true
	case "group", "supergroup":
		return a.cfg.AllowGroupChats
	default:
		return a.cfg.AllowGroupChats
	}
}

func (a *App) needsDangerConfirmation(text string) bool {
	return containsDangerousAction(text)
}

var dangerousActionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\brm\s+-[^\n]*(r|f)[^\n]*\s+/`),
	regexp.MustCompile(`(?i)\bgit\s+reset\s+--hard\b`),
	regexp.MustCompile(`(?i)\bgit\s+clean\s+-[^\n]*f`),
	regexp.MustCompile(`(?i)\bterraform\s+destroy\b`),
	regexp.MustCompile(`(?i)\bkubectl\s+delete\b`),
	regexp.MustCompile(`(?i)\baws\s+s3\s+rm\b`),
	regexp.MustCompile(`(?i)\bdrop\s+database\b`),
	regexp.MustCompile(`(?i)\bshutdown\b|\breboot\b`),
	regexp.MustCompile(`(?i)\bmkfs\b|\bdiskutil\s+erase`),
}

func containsDangerousAction(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	for _, pattern := range dangerousActionPatterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}\b`),
	regexp.MustCompile(`\b01[016789][-\s]?\d{3,4}[-\s]?\d{4}\b`),
	regexp.MustCompile(`(?:\+1[-.\s]?)?(?:\(\d{3}\)[-.\s]?|\b\d{3}[-.\s])\d{3}[-.\s]?\d{4}\b`),
	regexp.MustCompile(`\b\d{6,}:[A-Za-z0-9_-]{20,}\b`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`),
	regexp.MustCompile(`(?i)\b(?:aws_access_key_id|aws_secret_access_key|api[_-]?key|token|password)\s*[:=]\s*['"]?[^'"\s]+`),
}

func redactSecrets(text string) string {
	for _, pattern := range secretPatterns {
		text = pattern.ReplaceAllStringFunc(text, func(match string) string {
			if strings.Contains(match, "@") {
				return "[redacted-email]"
			}
			if strings.Contains(match, ":") || strings.Contains(strings.ToLower(match), "key") || strings.Contains(strings.ToLower(match), "token") || strings.Contains(strings.ToLower(match), "password") {
				return "[redacted-secret]"
			}
			return "[redacted-phone]"
		})
	}
	return text
}
