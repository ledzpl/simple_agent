package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(`
# comment
TEST_DOTENV_TOKEN=abc123
TEST_DOTENV_SPACED = hello world # comment
TEST_DOTENV_SINGLE='quoted value'
TEST_DOTENV_DOUBLE="line\nnext"
export TEST_DOTENV_EXPORTED=yes
`), 0600); err != nil {
		t.Fatalf("write dotenv: %v", err)
	}

	t.Setenv("TEST_DOTENV_TOKEN", "")
	if err := os.Unsetenv("TEST_DOTENV_TOKEN"); err != nil {
		t.Fatalf("unset env: %v", err)
	}
	for _, key := range []string{"TEST_DOTENV_SPACED", "TEST_DOTENV_SINGLE", "TEST_DOTENV_DOUBLE", "TEST_DOTENV_EXPORTED"} {
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
	}

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv returned error: %v", err)
	}

	assertEnv(t, "TEST_DOTENV_TOKEN", "abc123")
	assertEnv(t, "TEST_DOTENV_SPACED", "hello world")
	assertEnv(t, "TEST_DOTENV_SINGLE", "quoted value")
	assertEnv(t, "TEST_DOTENV_DOUBLE", "line\nnext")
	assertEnv(t, "TEST_DOTENV_EXPORTED", "yes")
}

func TestLoadDotEnvDoesNotOverrideExistingEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("TEST_DOTENV_KEEP=file\n"), 0600); err != nil {
		t.Fatalf("write dotenv: %v", err)
	}

	t.Setenv("TEST_DOTENV_KEEP", "process")
	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv returned error: %v", err)
	}
	assertEnv(t, "TEST_DOTENV_KEEP", "process")
}

func TestLoadDotEnvMissingFile(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), ".env")); err != nil {
		t.Fatalf("missing dotenv should be ignored: %v", err)
	}
}

func assertEnv(t *testing.T, key, want string) {
	t.Helper()
	if got := os.Getenv(key); got != want {
		t.Fatalf("%s mismatch\nwant: %q\n got: %q", key, want, got)
	}
}
