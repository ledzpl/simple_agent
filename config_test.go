package main

import (
	"reflect"
	"testing"
)

func TestParseAllowedChatIDs(t *testing.T) {
	got, err := parseAllowedChatIDs("123, -456, 123")
	if err != nil {
		t.Fatalf("parseAllowedChatIDs returned error: %v", err)
	}
	want := map[int64]struct{}{
		123:  {},
		-456: {},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ids mismatch\nwant: %#v\n got: %#v", want, got)
	}
}

func TestParseAllowedChatIDsInvalid(t *testing.T) {
	if _, err := parseAllowedChatIDs("123,nope"); err == nil {
		t.Fatal("expected invalid chat id error")
	}
}

func TestNormalizeTelegramParseMode(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"markdown":    "Markdown",
		"MarkdownV2":  "MarkdownV2",
		"markdown_v2": "MarkdownV2",
		"html":        "HTML",
		"plain":       "",
	}
	for input, want := range cases {
		if got := normalizeTelegramParseMode(input); got != want {
			t.Fatalf("normalizeTelegramParseMode(%q) = %q, want %q", input, got, want)
		}
	}
}
