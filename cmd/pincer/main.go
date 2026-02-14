package main

import (
	"bufio"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	charmLog "github.com/charmbracelet/log"
	"github.com/lox/pincer/internal/server"
)

type cliConfig struct {
	HTTPAddr          string `name:"http-addr" help:"HTTP listen address." env:"PINCER_HTTP_ADDR" default:":8080"`
	DBPath            string `name:"db-path" help:"SQLite database path." env:"PINCER_DB_PATH" default:"./pincer.db"`
	TokenHMACKey      string `name:"token-hmac-key" help:"HMAC key for bearer token signing." env:"PINCER_TOKEN_HMAC_KEY"`
	OpenRouterAPIKey  string `name:"openrouter-api-key" help:"OpenRouter API key." env:"OPENROUTER_API_KEY"`
	OpenRouterBaseURL string `name:"openrouter-base-url" help:"OpenRouter API base URL." env:"OPENROUTER_BASE_URL"`
	ModelPrimary      string `name:"model-primary" help:"Primary model ID." env:"PINCER_MODEL_PRIMARY" default:"anthropic/claude-opus-4.6"`
	ModelFallback     string `name:"model-fallback" help:"Fallback model ID." env:"PINCER_MODEL_FALLBACK"`
	LogLevel          string `name:"log-level" help:"Server log level." env:"PINCER_LOG_LEVEL" default:"info" enum:"debug,info,warn,error,fatal"`
	LogFormat         string `name:"log-format" help:"Log output format." env:"PINCER_LOG_FORMAT" default:"text" enum:"text,json"`
}

func main() {
	if err := loadDotEnvFile(".env"); err != nil {
		fmt.Fprintf(os.Stderr, "load .env: %v\n", err)
		os.Exit(1)
	}

	cfg, err := parseCLI(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse args: %v\n", err)
		os.Exit(2)
	}

	logger, err := newLogger(cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		fmt.Fprintf(os.Stderr, "configure logger: %v\n", err)
		os.Exit(2)
	}
	charmLog.SetDefault(logger)

	app, err := server.New(server.AppConfig{
		DBPath:            cfg.DBPath,
		TokenHMACKey:      cfg.TokenHMACKey,
		OpenRouterAPIKey:  cfg.OpenRouterAPIKey,
		OpenRouterBaseURL: cfg.OpenRouterBaseURL,
		ModelPrimary:      cfg.ModelPrimary,
		ModelFallback:     cfg.ModelFallback,
		Logger:            logger.With("component", "server"),
	})
	if err != nil {
		logger.Fatal("init app", "error", err)
	}
	defer app.Close()

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	logger.Info(
		"pincer listening",
		"addr", cfg.HTTPAddr,
		"db_path", cfg.DBPath,
		"openrouter_enabled", cfg.OpenRouterAPIKey != "",
		"model_primary", cfg.ModelPrimary,
	)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatal("listen and serve", "error", err)
	}
}

func parseCLI(args []string) (cliConfig, error) {
	var cfg cliConfig

	parser, err := kong.New(
		&cfg,
		kong.Name("pincer"),
		kong.Description("Pincer backend server"),
		kong.UsageOnError(),
	)
	if err != nil {
		return cliConfig{}, err
	}
	if _, err := parser.Parse(args); err != nil {
		return cliConfig{}, err
	}

	// Backward compatibility for earlier PINCER_OPENROUTER_* env names.
	cfg.OpenRouterAPIKey = firstNonEmpty(cfg.OpenRouterAPIKey, envFirst("PINCER_OPENROUTER_API_KEY"))
	cfg.OpenRouterBaseURL = firstNonEmpty(cfg.OpenRouterBaseURL, envFirst("PINCER_OPENROUTER_BASE_URL"))

	return cfg, nil
}

func newLogger(levelRaw, formatRaw string) (*charmLog.Logger, error) {
	level, err := charmLog.ParseLevel(strings.TrimSpace(levelRaw))
	if err != nil {
		return nil, err
	}

	formatter := charmLog.TextFormatter
	if strings.EqualFold(strings.TrimSpace(formatRaw), "json") {
		formatter = charmLog.JSONFormatter
	}

	return charmLog.NewWithOptions(os.Stderr, charmLog.Options{
		Prefix:          "pincer",
		Level:           level,
		ReportTimestamp: true,
		TimeFormat:      time.RFC3339,
		Formatter:       formatter,
	}), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func envFirst(keys ...string) string {
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return ""
}

func loadDotEnvFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		key, value, ok, parseErr := parseDotEnvLine(scanner.Text())
		if parseErr != nil {
			return fmt.Errorf("%s:%d: %w", path, lineNum, parseErr)
		}
		if !ok {
			continue
		}
		if os.Getenv(key) != "" {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set env %s: %w", key, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func parseDotEnvLine(line string) (key, value string, ok bool, err error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false, nil
	}

	if strings.HasPrefix(trimmed, "export ") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
	}

	parts := strings.SplitN(trimmed, "=", 2)
	if len(parts) != 2 {
		return "", "", false, fmt.Errorf("invalid .env line")
	}

	key = strings.TrimSpace(parts[0])
	if key == "" {
		return "", "", false, fmt.Errorf("empty key in .env line")
	}

	value = strings.TrimSpace(parts[1])
	parsedValue, err := parseDotEnvValue(value)
	if err != nil {
		return "", "", false, err
	}
	return key, parsedValue, true, nil
}

func parseDotEnvValue(raw string) (string, error) {
	if len(raw) >= 2 && strings.HasPrefix(raw, "\"") && strings.HasSuffix(raw, "\"") {
		value, err := strconv.Unquote(raw)
		if err != nil {
			return "", fmt.Errorf("invalid double-quoted value: %w", err)
		}
		return value, nil
	}
	if len(raw) >= 2 && strings.HasPrefix(raw, "'") && strings.HasSuffix(raw, "'") {
		return strings.TrimSuffix(strings.TrimPrefix(raw, "'"), "'"), nil
	}
	return raw, nil
}
