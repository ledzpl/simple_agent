package main

import (
	"log"
	"os"

	"telegram-local-agent/internal/app"
)

func main() {
	if err := app.Run(os.Args[1:]); err != nil {
		log.Fatalf("telegram-local-agent: %v", err)
	}
}
