package app

import (
	"reflect"
	"strings"
	"testing"
	"time"
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

func TestValidateAgentConfigRejectsDangerFullAccess(t *testing.T) {
	err := validateAgentConfig(Config{
		AgentBackend: BackendCodex,
		AgentTimeout: time.Second,
		CodexBin:     "codex",
		CodexSandbox: "danger-full-access",
	})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected danger-full-access rejection, got %v", err)
	}
}

func TestNormalizeCodexSandboxMapsLegacySeatbeltToReadOnly(t *testing.T) {
	if got := normalizeCodexSandbox("seatbelt"); got != "read-only" {
		t.Fatalf("normalizeCodexSandbox(seatbelt) = %q", got)
	}
}

func TestEnvParsersRejectInvalidValues(t *testing.T) {
	t.Setenv("TEST_BOOL", "sometimes")
	if _, err := envBool("TEST_BOOL", false); err == nil {
		t.Fatal("expected invalid boolean error")
	}
	t.Setenv("TEST_DURATION", "later")
	if _, err := envDuration("TEST_DURATION", time.Minute); err == nil {
		t.Fatal("expected invalid duration error")
	}
}
