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

func TestParseAllowedUserIDs(t *testing.T) {
	got, err := parseAllowedUserIDs("7, 8")
	if err != nil {
		t.Fatalf("parseAllowedUserIDs returned error: %v", err)
	}
	if _, ok := got[7]; !ok {
		t.Fatalf("missing user id 7: %#v", got)
	}
	if _, ok := got[8]; !ok {
		t.Fatalf("missing user id 8: %#v", got)
	}
}

func TestValidateCommandAllowed(t *testing.T) {
	allowlist, err := parseCommandAllowlist("chatgpt,/opt/bin/assistant")
	if err != nil {
		t.Fatalf("parseCommandAllowlist returned error: %v", err)
	}
	if err := validateCommandAllowed([]string{"/usr/local/bin/chatgpt"}, allowlist); err != nil {
		t.Fatalf("expected basename allowlist match: %v", err)
	}
	if err := validateCommandAllowed([]string{"/tmp/unknown"}, allowlist); err == nil {
		t.Fatal("expected disallowed command error")
	}
}
