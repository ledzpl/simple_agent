package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func Run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "--check-config":
			return checkConfig()
		case "-h", "--help":
			fmt.Println("usage: telegram-local-agent [--check-config]")
			return nil
		}
	}

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
	state, err := NewStateStore(cfg.StateDir)
	if err != nil {
		return err
	}

	bot := NewTelegramBot(cfg.TelegramToken)
	app := NewAppWithRouter(cfg, bot, router, memory)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.UseStateStore(ctx, state); err != nil {
		return err
	}

	log.Printf("starting telegram-local-agent agents=%d default_agent=%s debate=%t allowed_chats=%d", len(router.Runners()), router.Default().Name, cfg.DebateEnabled, len(cfg.AllowedChatIDs))
	if cfg.MemoryEnabled {
		log.Printf("memory enabled dir=%s", cfg.MemoryDir)
	} else {
		log.Printf("memory disabled")
	}
	if len(cfg.AllowedChatIDs) == 0 {
		log.Printf("TELEGRAM_ALLOWED_CHAT_IDS is empty; only /id and /start will be answered")
	}

	if err := app.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("run app: %w", err)
	}
	return nil
}

func checkConfig() error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	router, err := NewAgentRouter(cfg)
	if err != nil {
		return err
	}
	fmt.Printf("config ok\nagents: %d\ndefault_agent: %s\nbackend: %s\nallowed_chats: %d\nallowed_users: %d\nallow_groups: %t\ndebate: %t\nmemory: %t\nstate_dir: %s\n",
		len(router.Runners()),
		router.Default().Name,
		cfg.AgentBackend,
		len(cfg.AllowedChatIDs),
		len(cfg.AllowedUserIDs),
		cfg.AllowGroupChats,
		cfg.DebateEnabled,
		cfg.MemoryEnabled,
		cfg.StateDir,
	)
	return nil
}
