package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	charmLog "github.com/charmbracelet/log"
	"github.com/lox/pincer/internal/agent"
	"github.com/lox/pincer/internal/server"
	"golang.org/x/oauth2"
	_ "modernc.org/sqlite"
	"tailscale.com/tsnet"
)

type cli struct {
	LogLevel  string `name:"log-level" help:"Log level." env:"PINCER_LOG_LEVEL" default:"info" enum:"debug,info,warn,error,fatal"`
	LogFormat string `name:"log-format" help:"Log output format." env:"PINCER_LOG_FORMAT" default:"text" enum:"text,json"`

	Serve  serveCmd  `cmd:"" help:"Start the backend server." default:"withargs"`
	Thread threadCmd `cmd:"" help:"Display a thread's messages."`
	Google googleCmd `cmd:"" help:"Google integration commands."`
}

// serveCmd contains all flags for running the server.
type serveCmd struct {
	HTTPAddr           string `name:"http-addr" help:"HTTP listen address." env:"PINCER_HTTP_ADDR" default:":8080"`
	DBPath             string `name:"db-path" help:"SQLite database path." env:"PINCER_DB_PATH" default:"./pincer.db"`
	TokenHMACKey       string `name:"token-hmac-key" help:"HMAC key for bearer token signing." env:"PINCER_TOKEN_HMAC_KEY"`
	OpenRouterAPIKey   string `name:"openrouter-api-key" help:"OpenRouter API key." env:"OPENROUTER_API_KEY"`
	OpenRouterBaseURL  string `name:"openrouter-base-url" help:"OpenRouter API base URL." env:"OPENROUTER_BASE_URL"`
	KagiAPIKey         string `name:"kagi-api-key" help:"Kagi API key for web search and summarization." env:"KAGI_API_KEY"`
	ModelPrimary       string `name:"model-primary" help:"Primary model ID." env:"PINCER_MODEL_PRIMARY" default:"anthropic/claude-opus-4.6"`
	ModelFallback      string `name:"model-fallback" help:"Fallback model ID." env:"PINCER_MODEL_FALLBACK"`
	GoogleClientID     string `name:"google-client-id" help:"Google OAuth client ID for token refresh." env:"GOOGLE_CLIENT_ID"`
	GoogleClientSecret string `name:"google-client-secret" help:"Google OAuth client secret for token refresh." env:"GOOGLE_CLIENT_SECRET"`
	TSHostname         string `name:"ts-hostname" help:"Tailscale hostname for tsnet." env:"TS_HOSTNAME" default:"pincer"`
	TSServiceName      string `name:"ts-service-name" help:"Tailscale service name (svc:<name>)." env:"TS_SERVICE_NAME" default:"pincer"`
	TSStateDir         string `name:"ts-state-dir" help:"Tailscale state directory." env:"TS_STATE_DIR" default:""`
}

// googleCmd groups Google integration subcommands.
type googleCmd struct {
	Login googleLoginCmd `cmd:"" help:"Authorize with Google via browser OAuth flow."`
	Auth  googleAuthCmd  `cmd:"" help:"Manually store an OAuth token (for testing/scripting)."`
}

// googleLoginCmd runs the OAuth authorization code flow.
type googleLoginCmd struct {
	DBPath       string `name:"db-path" help:"SQLite database path." env:"PINCER_DB_PATH" default:"./pincer.db"`
	Identity     string `name:"identity" help:"Token identity (user or bot)." enum:"user,bot" default:"user"`
	ClientID     string `name:"client-id" help:"Google OAuth client ID." env:"GOOGLE_CLIENT_ID" required:""`
	ClientSecret string `name:"client-secret" help:"Google OAuth client secret." env:"GOOGLE_CLIENT_SECRET" required:""`
	Port         int    `name:"port" help:"Local callback server port." default:"8085"`
	Scopes       string `name:"scopes" help:"Comma-separated OAuth scopes." default:"https://www.googleapis.com/auth/gmail.readonly,https://www.googleapis.com/auth/gmail.compose,https://www.googleapis.com/auth/gmail.send"`
}

// googleAuthCmd stores a Google OAuth token manually.
type googleAuthCmd struct {
	DBPath       string `name:"db-path" help:"SQLite database path." env:"PINCER_DB_PATH" default:"./pincer.db"`
	Identity     string `name:"identity" help:"Token identity (user or bot)." enum:"user,bot" required:""`
	AccessToken  string `name:"access-token" help:"Google OAuth access token." required:""`
	RefreshToken string `name:"refresh-token" help:"Google OAuth refresh token."`
	ExpiresIn    int    `name:"expires-in" help:"Token expiry in seconds from now." default:"3600"`
}

// threadCmd displays a thread's messages.
type threadCmd struct {
	ThreadID string `arg:"" help:"Thread ID to display."`
	DBPath   string `name:"db-path" help:"SQLite database path." env:"PINCER_DB_PATH" default:"./pincer.db"`
	Format   string `name:"format" help:"Output format." enum:"markdown,json" default:"markdown"`
	All      bool   `name:"all" help:"Include internal messages." default:"false"`
}

func (cmd *threadCmd) Run(globals *cli) error {
	db, err := sql.Open("sqlite", cmd.DBPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	// Load thread metadata.
	var channel, createdAt, title string
	err = db.QueryRow(`SELECT channel, created_at, COALESCE(title, '') FROM threads WHERE thread_id = ?`, cmd.ThreadID).
		Scan(&channel, &createdAt, &title)
	if err == sql.ErrNoRows {
		return fmt.Errorf("thread %q not found", cmd.ThreadID)
	}
	if err != nil {
		return fmt.Errorf("query thread: %w", err)
	}

	// Load messages.
	roleFilter := `AND role != 'internal'`
	if cmd.All {
		roleFilter = ""
	}
	rows, err := db.Query(fmt.Sprintf(`
		SELECT message_id, role, content, created_at
		FROM messages
		WHERE thread_id = ? %s
		ORDER BY created_at ASC
	`, roleFilter), cmd.ThreadID)
	if err != nil {
		return fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	type message struct {
		MessageID string `json:"message_id"`
		Role      string `json:"role"`
		Content   string `json:"content"`
		CreatedAt string `json:"created_at"`
	}

	var messages []message
	for rows.Next() {
		var m message
		if err := rows.Scan(&m.MessageID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return fmt.Errorf("scan message: %w", err)
		}
		messages = append(messages, m)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate messages: %w", err)
	}

	if cmd.Format == "json" {
		out := struct {
			ThreadID  string    `json:"thread_id"`
			Title     string    `json:"title,omitempty"`
			Channel   string    `json:"channel"`
			CreatedAt string    `json:"created_at"`
			Messages  []message `json:"messages"`
		}{
			ThreadID:  cmd.ThreadID,
			Title:     title,
			Channel:   channel,
			CreatedAt: createdAt,
			Messages:  messages,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	// Markdown output.
	if title != "" {
		fmt.Printf("# %s\n\n", title)
	} else {
		fmt.Printf("# Thread %s\n\n", cmd.ThreadID)
	}
	fmt.Printf("**Channel:** %s · **Created:** %s · **Messages:** %d\n\n---\n\n", channel, createdAt, len(messages))

	for _, m := range messages {
		roleLabel := strings.ToUpper(m.Role[:1]) + m.Role[1:]
		fmt.Printf("### %s\n", roleLabel)
		fmt.Printf("_%s_\n\n", m.CreatedAt)
		fmt.Printf("%s\n\n---\n\n", m.Content)
	}

	return nil
}

func (cmd *serveCmd) Run(globals *cli) error {
	logger, err := newLogger(globals.LogLevel, globals.LogFormat)
	if err != nil {
		return fmt.Errorf("configure logger: %w", err)
	}
	charmLog.SetDefault(logger)

	// Backward compatibility for earlier PINCER_OPENROUTER_* env names.
	cmd.OpenRouterAPIKey = firstNonEmpty(cmd.OpenRouterAPIKey, envFirst("PINCER_OPENROUTER_API_KEY"))
	cmd.OpenRouterBaseURL = firstNonEmpty(cmd.OpenRouterBaseURL, envFirst("PINCER_OPENROUTER_BASE_URL"))

	app, err := server.New(server.AppConfig{
		DBPath:             cmd.DBPath,
		TokenHMACKey:       cmd.TokenHMACKey,
		OpenRouterAPIKey:   cmd.OpenRouterAPIKey,
		OpenRouterBaseURL:  cmd.OpenRouterBaseURL,
		KagiAPIKey:         cmd.KagiAPIKey,
		GoogleClientID:     cmd.GoogleClientID,
		GoogleClientSecret: cmd.GoogleClientSecret,
		ModelPrimary:       cmd.ModelPrimary,
		ModelFallback:      cmd.ModelFallback,
		Logger:             logger.With("component", "server"),
	})
	if err != nil {
		return fmt.Errorf("init app: %w", err)
	}
	defer app.Close()

	handler := app.Handler()

	if os.Getenv("TS_AUTHKEY") != "" {
		tsLogger := logger.With("component", "tsnet")

		ts := &tsnet.Server{
			Hostname: cmd.TSHostname,
			UserLogf: func(format string, args ...any) { tsLogger.Infof(format, args...) },
			Logf:     func(format string, args ...any) { tsLogger.Debugf(format, args...) },
		}
		if cmd.TSStateDir != "" {
			ts.Dir = cmd.TSStateDir
		}
		defer ts.Close()

		svcName := "svc:" + cmd.TSServiceName
		ln, err := ts.ListenService(svcName, tsnet.ServiceModeHTTP{
			HTTPS: true,
			Port:  443,
		})
		if err != nil {
			return fmt.Errorf("tsnet listen service: %w", err)
		}
		defer ln.Close()

		tsLogger.Info("tailscale service listening",
			"hostname", cmd.TSHostname,
			"service", svcName,
			"fqdn", ln.FQDN,
		)

		go func() {
			tsServer := &http.Server{
				Handler:           handler,
				ReadHeaderTimeout: 10 * time.Second,
			}
			if err := tsServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				tsLogger.Fatal("tsnet serve", "error", err)
			}
		}()
	}

	httpServer := &http.Server{
		Addr:              cmd.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	logger.Info(
		"pincer listening",
		"addr", cmd.HTTPAddr,
		"db_path", cmd.DBPath,
		"openrouter_enabled", cmd.OpenRouterAPIKey != "",
		"kagi_enabled", cmd.KagiAPIKey != "",
		"model_primary", cmd.ModelPrimary,
	)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("listen and serve: %w", err)
	}
	return nil
}

func (cmd *googleLoginCmd) Run(globals *cli) error {
	logger, err := newLogger(globals.LogLevel, globals.LogFormat)
	if err != nil {
		return fmt.Errorf("configure logger: %w", err)
	}

	scopes := strings.Split(cmd.Scopes, ",")
	for i := range scopes {
		scopes[i] = strings.TrimSpace(scopes[i])
	}

	callbackAddr := fmt.Sprintf("127.0.0.1:%d", cmd.Port)
	redirectURL := fmt.Sprintf("http://%s/callback", callbackAddr)

	oauthConfig := &oauth2.Config{
		ClientID:     cmd.ClientID,
		ClientSecret: cmd.ClientSecret,
		Scopes:       scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://accounts.google.com/o/oauth2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
		},
		RedirectURL: redirectURL,
	}

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	callbackServer := &http.Server{Addr: callbackAddr, Handler: mux}

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			errCh <- fmt.Errorf("no authorization code in callback")
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body style="font-family:system-ui;text-align:center;padding:60px">
<h1>✓ Authorization Successful</h1>
<p>You can close this window and return to the terminal.</p>
</body></html>`)
		codeCh <- code
	})

	authURL := oauthConfig.AuthCodeURL("pincer-oauth", oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))
	fmt.Printf("\n=== Google OAuth Authorization ===\n")
	fmt.Printf("Open this URL in your browser:\n\n%s\n\n", authURL)
	fmt.Printf("Waiting for authorization (timeout: 5 minutes)...\n")

	go func() {
		if err := callbackServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		_ = callbackServer.Close()
		return fmt.Errorf("oauth callback: %w", err)
	case <-time.After(5 * time.Minute):
		_ = callbackServer.Close()
		return fmt.Errorf("authorization timed out after 5 minutes")
	}

	// Brief delay so the success page renders before shutdown.
	time.Sleep(500 * time.Millisecond)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = callbackServer.Shutdown(shutdownCtx)

	oauthToken, err := oauthConfig.Exchange(context.Background(), code)
	if err != nil {
		return fmt.Errorf("exchange authorization code: %w", err)
	}

	token := agent.OAuthToken{
		Identity:     cmd.Identity,
		Provider:     "google",
		AccessToken:  oauthToken.AccessToken,
		RefreshToken: oauthToken.RefreshToken,
		TokenType:    oauthToken.TokenType,
		Expiry:       oauthToken.Expiry.UTC(),
		Scopes:       scopes,
	}

	if err := storeOAuthTokenToDB(cmd.DBPath, cmd.Identity, token); err != nil {
		return err
	}

	logger.Info("google oauth token stored",
		"identity", cmd.Identity,
		"db_path", cmd.DBPath,
		"has_refresh_token", oauthToken.RefreshToken != "",
		"expiry", oauthToken.Expiry.Format(time.RFC3339),
		"scopes", strings.Join(scopes, ","),
	)
	return nil
}

func (cmd *googleAuthCmd) Run(globals *cli) error {
	logger, err := newLogger(globals.LogLevel, globals.LogFormat)
	if err != nil {
		return fmt.Errorf("configure logger: %w", err)
	}

	token := agent.OAuthToken{
		Identity:     cmd.Identity,
		Provider:     "google",
		AccessToken:  cmd.AccessToken,
		RefreshToken: cmd.RefreshToken,
		TokenType:    "Bearer",
		Expiry:       time.Now().UTC().Add(time.Duration(cmd.ExpiresIn) * time.Second),
	}

	if err := storeOAuthTokenToDB(cmd.DBPath, cmd.Identity, token); err != nil {
		return err
	}

	logger.Info("google oauth token stored",
		"identity", cmd.Identity,
		"db_path", cmd.DBPath,
		"expires_in", fmt.Sprintf("%ds", cmd.ExpiresIn),
		"has_refresh_token", cmd.RefreshToken != "",
	)
	return nil
}

func storeOAuthTokenToDB(dbPath, identity string, token agent.OAuthToken) error {
	tokenJSON, err := json.Marshal(token)
	if err != nil {
		return fmt.Errorf("marshal token: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
		return fmt.Errorf("enable wal: %w", err)
	}

	if _, err := db.ExecContext(context.Background(), `CREATE TABLE IF NOT EXISTS oauth_tokens(
		user_id TEXT NOT NULL,
		identity TEXT NOT NULL,
		provider TEXT NOT NULL,
		token_json TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY(user_id, identity, provider)
	)`); err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	ownerID := "owner-dev"

	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO oauth_tokens(user_id, identity, provider, token_json, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, identity, provider) DO UPDATE SET
			token_json = excluded.token_json,
			updated_at = excluded.updated_at
	`, ownerID, identity, "google", string(tokenJSON), now, now); err != nil {
		return fmt.Errorf("store token: %w", err)
	}

	return nil
}

func main() {
	if err := loadDotEnvFile(".env"); err != nil {
		fmt.Fprintf(os.Stderr, "load .env: %v\n", err)
		os.Exit(1)
	}

	var app cli
	ctx := kong.Parse(&app,
		kong.Name("pincer"),
		kong.Description("Pincer backend server and management CLI."),
		kong.UsageOnError(),
	)
	if err := ctx.Run(&app); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
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
