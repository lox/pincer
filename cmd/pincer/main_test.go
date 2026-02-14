package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseCLIUsesLegacyOpenRouterEnvFallback(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("PINCER_OPENROUTER_API_KEY", "legacy-key")
	t.Setenv("OPENROUTER_BASE_URL", "")
	t.Setenv("PINCER_OPENROUTER_BASE_URL", "https://legacy-base.example")

	cfg, err := parseCLI(nil)
	if err != nil {
		t.Fatalf("parse cli: %v", err)
	}

	if cfg.OpenRouterAPIKey != "legacy-key" {
		t.Fatalf("expected legacy api key fallback, got %q", cfg.OpenRouterAPIKey)
	}
	if cfg.OpenRouterBaseURL != "https://legacy-base.example" {
		t.Fatalf("expected legacy base url fallback, got %q", cfg.OpenRouterBaseURL)
	}
}

func TestParseCLIPrefersFlagOverLegacyEnv(t *testing.T) {
	t.Setenv("PINCER_OPENROUTER_API_KEY", "legacy-key")

	cfg, err := parseCLI([]string{"--openrouter-api-key=flag-key"})
	if err != nil {
		t.Fatalf("parse cli: %v", err)
	}
	if cfg.OpenRouterAPIKey != "flag-key" {
		t.Fatalf("expected flag to override legacy env, got %q", cfg.OpenRouterAPIKey)
	}
}

func TestNewLoggerInvalidLevel(t *testing.T) {
	if _, err := newLogger("nope", "text"); err == nil {
		t.Fatalf("expected invalid log level error")
	}
}

func TestParseDotEnvLine(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		key     string
		value   string
		ok      bool
		wantErr bool
	}{
		{name: "empty", line: "", ok: false},
		{name: "comment", line: "# comment", ok: false},
		{name: "simple", line: "OPENROUTER_API_KEY=abc123", key: "OPENROUTER_API_KEY", value: "abc123", ok: true},
		{name: "export", line: "export OPENROUTER_API_KEY=abc123", key: "OPENROUTER_API_KEY", value: "abc123", ok: true},
		{name: "double quoted", line: "OPENROUTER_API_KEY=\"abc 123\"", key: "OPENROUTER_API_KEY", value: "abc 123", ok: true},
		{name: "single quoted", line: "OPENROUTER_API_KEY='abc 123'", key: "OPENROUTER_API_KEY", value: "abc 123", ok: true},
		{name: "invalid", line: "OPENROUTER_API_KEY", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key, value, ok, err := parseDotEnvLine(tc.line)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok != tc.ok {
				t.Fatalf("ok mismatch: got=%v want=%v", ok, tc.ok)
			}
			if key != tc.key {
				t.Fatalf("key mismatch: got=%q want=%q", key, tc.key)
			}
			if value != tc.value {
				t.Fatalf("value mismatch: got=%q want=%q", value, tc.value)
			}
		})
	}
}

func TestLoadDotEnvFileSetsMissingValuesOnly(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("PINCER_MODEL_PRIMARY", "")

	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "OPENROUTER_API_KEY=from-dotenv\nPINCER_MODEL_PRIMARY=anthropic/claude-opus-4.6\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	if err := loadDotEnvFile(path); err != nil {
		t.Fatalf("load .env: %v", err)
	}

	if got := os.Getenv("OPENROUTER_API_KEY"); got != "from-dotenv" {
		t.Fatalf("OPENROUTER_API_KEY mismatch: got=%q", got)
	}
	if got := os.Getenv("PINCER_MODEL_PRIMARY"); got != "anthropic/claude-opus-4.6" {
		t.Fatalf("PINCER_MODEL_PRIMARY mismatch: got=%q", got)
	}
}

func TestLoadDotEnvFileDoesNotOverrideExistingValues(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "already-set")

	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("OPENROUTER_API_KEY=from-dotenv\n"), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	if err := loadDotEnvFile(path); err != nil {
		t.Fatalf("load .env: %v", err)
	}

	if got := os.Getenv("OPENROUTER_API_KEY"); got != "already-set" {
		t.Fatalf("expected env to remain already-set, got=%q", got)
	}
}
