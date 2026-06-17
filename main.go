package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("telegram-local-agent: %v", err)
	}
}

func run() error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}

	router, err := NewAgentRouter(cfg)
	if err != nil {
		return err
	}

	memory, err := NewMemoryStore(cfg)
	if err != nil {
		return err
	}

	bot := NewTelegramBot(cfg.TelegramToken)
	app := NewAppWithRouter(cfg, bot, router, memory)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("starting telegram-local-agent agents=%d default_agent=%s debate=%t allowed_chats=%d", len(router.Runners()), router.Default().Name, cfg.DebateEnabled, len(cfg.AllowedChatIDs))
	if cfg.MemoryEnabled {
		log.Printf("memory enabled dir=%s max_messages=%d max_chars=%d", cfg.MemoryDir, cfg.MemoryMaxMessages, cfg.MemoryMaxChars)
	} else {
		log.Printf("memory disabled")
	}
	if len(cfg.AllowedChatIDs) == 0 && !cfg.AllowAllChats {
		log.Printf("TELEGRAM_ALLOWED_CHAT_IDS is empty; only /id and /start will be answered")
	}

	if err := app.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("run app: %w", err)
	}
	return nil
}
