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
