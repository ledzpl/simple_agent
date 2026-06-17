package main

import (
	"strings"
	"testing"
)

func TestHelpMessageIncludesDefinedCommands(t *testing.T) {
	help := helpMessage()
	for _, command := range commandHelps {
		if !strings.Contains(help, command.Usage) {
			t.Fatalf("help message missing command %q:\n%s", command.Usage, help)
		}
		if !strings.Contains(help, command.Description) {
			t.Fatalf("help message missing description %q:\n%s", command.Description, help)
		}
	}
}
