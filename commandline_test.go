package main

import (
	"reflect"
	"testing"
)

func TestSplitCommandLine(t *testing.T) {
	got, err := splitCommandLine(`chatgpt --model "gpt test" 'hello world' plain\ arg`)
	if err != nil {
		t.Fatalf("splitCommandLine returned error: %v", err)
	}
	want := []string{"chatgpt", "--model", "gpt test", "hello world", "plain arg"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args mismatch\nwant: %#v\n got: %#v", want, got)
	}
}

func TestSplitCommandLineUnterminatedQuote(t *testing.T) {
	if _, err := splitCommandLine(`chatgpt "oops`); err == nil {
		t.Fatal("expected unterminated quote error")
	}
}
