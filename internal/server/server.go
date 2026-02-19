package server

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	charmLog "github.com/charmbracelet/log"
	protocolv1 "github.com/lox/pincer/gen/proto/pincer/protocol/v1"
	"github.com/lox/pincer/internal/agent"
	_ "modernc.org/sqlite"
)

const (
	defaultOwnerID                    = "owner-dev"
	defaultTokenHMACKey               = "pincer-dev-token-hmac-key-change-me"
	defaultPrimaryModel               = "anthropic/claude-opus-4.6"
	defaultAssistantMessage           = "Message processed."
	defaultActionJustification        = "User requested external follow-up"
	defaultActionExpiry               = 24 * time.Hour
	defaultPlannerHistoryLimit        = 30
	maxProposedActionsPerTurn         = 3
	defaultActionExecutorPollInterval = 250 * time.Millisecond
	defaultTokenTTL                   = 30 * 24 * time.Hour
	defaultTokenRenewWindow           = 7 * 24 * time.Hour
	defaultPairingCodeTTL             = 10 * time.Minute
	lastUsedUpdateInterval            = time.Hour
	defaultBashExecTimeout            = 10 * time.Second
	maxBashExecTimeout                = 15 * time.Minute
	maxBashOutputBytes                = 8 * 1024
	maxBashSystemMessageChars         = 4 * 1024
	maxInlineToolSteps                = 10
)

var errIdempotencyConflict = errors.New("idempotency conflict")

type AppConfig struct {
	DBPath                  string
	TokenHMACKey            string
	OpenRouterAPIKey        string
	OpenRouterBaseURL       string
	KagiAPIKey              string
	ModelPrimary            string
	ModelFallback           string
	Logger                  *charmLog.Logger
	Planner                 agent.Planner
	WebFetcher              *agent.WebFetcher
	ImageDescriber          *agent.ImageDescriber
	ActionExecutorInterval  time.Duration
	DisableBackgroundWorker bool
}

type App struct {
	db                     *sql.DB
	tokenHMACKey           []byte
	logger                 *charmLog.Logger
	planner                agent.Planner
	kagiAPIKey             string
	webFetcher             *agent.WebFetcher
	imageDescriber         *agent.ImageDescriber
	imageProxyRewriter     *agent.ImageProxyRewriter
	gmailClient *agent.GmailClient
	ownerID     string
	llmConfigured            bool
	stopCh                 chan struct{}
	doneCh                 chan struct{}
	closeOnce              sync.Once
	actionExecutorInterval time.Duration
	eventAppendMu          sync.Mutex
	eventSubsMu            sync.RWMutex
	eventSubs              map[string]map[chan *threadEvent]struct{}
	resumingTurns          sync.Map // turnID → struct{}: guards against double-resumption
}

type threadResponse struct {
	ThreadID string `json:"thread_id"`
}

type createMessageRequest struct {
	Content string `json:"content"`
}

type createMessageResponse struct {
	AssistantMessage string `json:"assistant_message"`
	ActionID         string `json:"action_id"`
}

type createPairingCodeResponse struct {
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"`
}

type bindPairingRequest struct {
	Code       string `json:"code"`
	DeviceName string `json:"device_name"`
	PublicKey  string `json:"public_key"`
}

type bindPairingResponse struct {
	DeviceID  string `json:"device_id"`
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

type rejectActionRequest struct {
	Reason string `json:"reason"`
}

type message struct {
	MessageID string `json:"message_id"`
	ThreadID  string `json:"thread_id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

type messagesResponse struct {
	Items []message `json:"items"`
}

type approval struct {
	ActionID        string `json:"action_id"`
	Source          string `json:"source"`
	SourceID        string `json:"source_id"`
	Tool            string `json:"tool"`
	Status          string `json:"status"`
	RiskClass       string `json:"risk_class"`
	IdempotencyKey  string `json:"idempotency_key"`
	Justification   string `json:"justification"`
	CreatedAt       string `json:"created_at"`
	ExpiresAt       string `json:"expires_at"`
	RejectionReason string `json:"rejection_reason,omitempty"`
	ArgsJSON        string `json:"args_json,omitempty"`
	UserID          string `json:"user_id,omitempty"`
}

type bashActionArgs struct {
	Command   string `json:"command"`
	CWD       string `json:"cwd,omitempty"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
}

type bashExecutionResult struct {
	Command   string
	CWD       string
	Timeout   time.Duration
	Duration  time.Duration
	ExitCode  int
	Output    string
	TimedOut  bool
	Truncated bool
}

type approvalsResponse struct {
	Items []approval `json:"items"`
}

type auditEvent struct {
	EntryID   string `json:"entry_id"`
	EventType string `json:"event_type"`
	EntityID  string `json:"entity_id"`
	Payload   string `json:"payload_json"`
	CreatedAt string `json:"created_at"`
}

type auditResponse struct {
	Items []auditEvent `json:"items"`
}

type device struct {
	DeviceID  string `json:"device_id"`
	Name      string `json:"name"`
	RevokedAt string `json:"revoked_at"`
	CreatedAt string `json:"created_at"`
	IsCurrent bool   `json:"is_current"`
}

type devicesResponse struct {
	Items []device `json:"items"`
}

func New(cfg AppConfig) (*App, error) {
	if cfg.DBPath == "" {
		return nil, errors.New("db path is required")
	}

	logger := cfg.Logger
	if logger == nil {
		logger = charmLog.NewWithOptions(os.Stderr, charmLog.Options{
			Prefix:          "pincer",
			Level:           charmLog.InfoLevel,
			ReportTimestamp: true,
			TimeFormat:      time.RFC3339,
		})
	}

	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable wal: %w", err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	planner := cfg.Planner
	if planner == nil {
		staticPlanner := agent.NewStaticPlanner()
		planner = staticPlanner

		if cfg.OpenRouterAPIKey != "" {
			primaryModel := strings.TrimSpace(cfg.ModelPrimary)
			if primaryModel == "" {
				primaryModel = defaultPrimaryModel
			}

			openAIPlanner, err := agent.NewOpenAIPlanner(agent.OpenAIPlannerConfig{
				APIKey:        cfg.OpenRouterAPIKey,
				BaseURL:       cfg.OpenRouterBaseURL,
				PrimaryModel:  primaryModel,
				FallbackModel: cfg.ModelFallback,
				UserAgent:     "pincer/0.1",
			})
			if err != nil {
				_ = db.Close()
				return nil, fmt.Errorf("init planner: %w", err)
			}

			planner = agent.NewFallbackPlanner(openAIPlanner, staticPlanner)
		}
	}

	tokenHMACKey := cfg.TokenHMACKey
	if tokenHMACKey == "" {
		tokenHMACKey = defaultTokenHMACKey
	}

	interval := cfg.ActionExecutorInterval
	if interval <= 0 {
		interval = defaultActionExecutorPollInterval
	}

	webFetcher := cfg.WebFetcher
	if webFetcher == nil {
		webFetcher = agent.NewWebFetcher()
	}

	imageDescriber := cfg.ImageDescriber
	if imageDescriber == nil && cfg.OpenRouterAPIKey != "" {
		imageDescriber = agent.NewImageDescriber(cfg.OpenRouterAPIKey, cfg.OpenRouterBaseURL)
	}

	imageProxyRewriter := agent.NewImageProxyRewriter([]byte(tokenHMACKey), "/proxy/image")

	gmailClient := agent.NewGmailClient()

	app := &App{
		db:                 db,
		tokenHMACKey:       []byte(tokenHMACKey),
		logger:             logger,
		planner:            planner,
		kagiAPIKey:         strings.TrimSpace(cfg.KagiAPIKey),
		webFetcher:         webFetcher,
		imageDescriber:     imageDescriber,
		imageProxyRewriter: imageProxyRewriter,
		gmailClient:        gmailClient,
		ownerID:            defaultOwnerID,
		llmConfigured:          cfg.OpenRouterAPIKey != "" || cfg.Planner != nil,
		stopCh:                 make(chan struct{}),
		doneCh:                 make(chan struct{}),
		actionExecutorInterval: interval,
		eventSubs:              make(map[string]map[chan *threadEvent]struct{}),
	}

	if !cfg.DisableBackgroundWorker {
		go app.runActionExecutor()
	} else {
		close(app.doneCh)
	}

	return app, nil
}

func (a *App) Close() error {
	var closeErr error
	a.closeOnce.Do(func() {
		close(a.stopCh)
		<-a.doneCh
		closeErr = a.db.Close()
	})
	return closeErr
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	a.registerConnectHandlers(mux)
	mux.HandleFunc("/proxy/image", a.handleImageProxy)
	mux.HandleFunc("/proxy/gmail/attachment", a.handleGmailAttachmentProxy)
	return a.loggingMiddleware(a.authMiddleware(mux))
}

const (
	maxProxyImageBytes   = 10 * 1024 * 1024 // 10 MB
	proxyImageTimeout    = 30 * time.Second
	maxProxyContentLen   = 512
)

func (a *App) handleImageProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rawURL := r.URL.Query().Get("url")
	sig := r.URL.Query().Get("sig")
	if rawURL == "" || sig == "" {
		http.Error(w, "missing url or sig parameter", http.StatusBadRequest)
		return
	}

	if !agent.VerifyURL(a.tokenHMACKey, rawURL, sig) {
		http.Error(w, "invalid signature", http.StatusForbidden)
		return
	}

	// Try serving from cache first.
	if a.serveCachedImage(w, sig) {
		return
	}

	// Cache miss — fetch from upstream.
	ctx, cancel := context.WithTimeout(r.Context(), proxyImageTimeout)
	defer cancel()

	body, ct, statusCode, err := a.webFetcher.FetchRawImage(ctx, rawURL, maxProxyImageBytes)
	if err != nil {
		a.logger.Warn("image proxy fetch failed", "url", rawURL, "error", err)
		http.Error(w, "fetch failed", http.StatusBadGateway)
		return
	}

	if statusCode < 200 || statusCode >= 300 {
		http.Error(w, fmt.Sprintf("upstream status %d", statusCode), http.StatusBadGateway)
		return
	}

	if !isAllowedImageMIME(ct) {
		a.logger.Warn("image proxy blocked disallowed MIME type", "url", rawURL, "content_type", ct)
		http.Error(w, "disallowed content type", http.StatusForbidden)
		return
	}

	// Cache for future requests.
	a.cacheImage(sig, rawURL, ct, body)

	a.serveImageBytes(w, ct, body)
}

func (a *App) serveCachedImage(w http.ResponseWriter, urlHash string) bool {
	var ct string
	var data []byte
	err := a.db.QueryRow(`SELECT content_type, data FROM cached_images WHERE url_hash = ?`, urlHash).Scan(&ct, &data)
	if err != nil {
		return false
	}
	a.serveImageBytes(w, ct, data)
	return true
}

func (a *App) cacheImage(urlHash, originalURL, contentType string, data []byte) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := a.db.Exec(`
		INSERT OR IGNORE INTO cached_images(url_hash, original_url, content_type, data, created_at)
		VALUES(?, ?, ?, ?, ?)
	`, urlHash, originalURL, contentType, data, now)
	if err != nil {
		a.logger.Warn("failed to cache image", "url", originalURL, "error", err)
	}
}

func (a *App) serveImageBytes(w http.ResponseWriter, ct string, data []byte) {
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'none'; script-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func isAllowedImageMIME(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch ct {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/avif":
		return true
	default:
		return false
	}
}

const maxGmailAttachmentBytes = 25 * 1024 * 1024 // 25 MB

func (a *App) handleGmailAttachmentProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	messageID := r.URL.Query().Get("message_id")
	attachmentID := r.URL.Query().Get("attachment_id")
	if messageID == "" || attachmentID == "" {
		http.Error(w, "missing message_id or attachment_id parameter", http.StatusBadRequest)
		return
	}

	oauthToken, err := a.loadOAuthToken(a.ownerID, "user", "google")
	if err != nil {
		a.logger.Warn("gmail attachment proxy: no oauth token", "error", err)
		http.Error(w, "oauth token not available", http.StatusUnauthorized)
		return
	}

	data, err := a.gmailClient.GetAttachment(r.Context(), oauthToken.AccessToken, agent.GmailGetAttachmentArgs{
		MessageID:    messageID,
		AttachmentID: attachmentID,
	})
	if err != nil {
		a.logger.Warn("gmail attachment proxy fetch failed", "error", err)
		http.Error(w, "fetch failed", http.StatusBadGateway)
		return
	}

	if len(data) > maxGmailAttachmentBytes {
		http.Error(w, "attachment too large", http.StatusBadGateway)
		return
	}

	ct := r.URL.Query().Get("mime_type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)

	if filename := r.URL.Query().Get("filename"); filename != "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	}

	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (a *App) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)

		statusCode := recorder.status()
		level := charmLog.InfoLevel
		switch {
		case statusCode >= http.StatusInternalServerError:
			level = charmLog.ErrorLevel
		case statusCode >= http.StatusBadRequest:
			level = charmLog.WarnLevel
		default:
			level = charmLog.DebugLevel
		}

		keyvals := []interface{}{
			"method", r.Method,
			"path", r.URL.Path,
			"status", statusCode,
			"duration_ms", time.Since(start).Milliseconds(),
			"response_bytes", recorder.bytesWritten,
		}
		if remoteAddr := clientIP(r.RemoteAddr); remoteAddr != "" {
			keyvals = append(keyvals, "remote_addr", remoteAddr)
		}
		if userAgent := r.UserAgent(); userAgent != "" {
			keyvals = append(keyvals, "user_agent", userAgent)
		}

		a.logger.Log(level, "http request", keyvals...)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode   int
	bytesWritten int
}

func (r *statusRecorder) WriteHeader(statusCode int) {
	r.statusCode = statusCode
	r.ResponseWriter.WriteHeader(statusCode)
}

func (r *statusRecorder) Write(data []byte) (int, error) {
	if r.statusCode == 0 {
		r.statusCode = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(data)
	r.bytesWritten += n
	return n, err
}

func (r *statusRecorder) status() int {
	if r.statusCode == 0 {
		return http.StatusOK
	}
	return r.statusCode
}

func (r *statusRecorder) Flush() {
	flusher, ok := r.ResponseWriter.(http.Flusher)
	if !ok {
		return
	}
	flusher.Flush()
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijacker not supported")
	}
	return hijacker.Hijack()
}

func (r *statusRecorder) Push(target string, opts *http.PushOptions) error {
	pusher, ok := r.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, opts)
}

func (a *App) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		rawToken := bearerTokenFromHeader(r.Header.Get("Authorization"))
		if rawToken == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		if err := a.validateAndTouchToken(rawToken); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) markActionApproved(actionID string) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var status string
	var expiresAtRaw string
	var source string
	var sourceID string
	var tool string
	err = tx.QueryRow(`
		SELECT status, expires_at, source, source_id, tool
		FROM proposed_actions
		WHERE action_id = ?
	`, actionID).Scan(&status, &expiresAtRaw, &source, &sourceID, &tool)
	if err != nil {
		return err
	}
	if status != "PENDING" {
		return fmt.Errorf("action is not pending")
	}

	expiresAt, err := parseTimestamp(expiresAtRaw)
	if err != nil {
		return fmt.Errorf("parse expires_at: %w", err)
	}
	nowTime := time.Now().UTC()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if !expiresAt.After(nowTime) {
		if _, err := tx.Exec(`
			UPDATE proposed_actions
			SET status = 'REJECTED', rejection_reason = 'expired'
			WHERE action_id = ? AND status = 'PENDING'
		`, actionID); err != nil {
			return err
		}
		if err := insertAuditTx(tx, "action_expired", actionID, `{"reason":"expired"}`, now); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		a.emitActionStatusEvent(context.Background(), source, sourceID, "", actionID, protocolv1.ActionStatus_REJECTED, "expired")
		return fmt.Errorf("action is expired")
	}

	if _, err := tx.Exec(`UPDATE proposed_actions SET status = 'APPROVED' WHERE action_id = ?`, actionID); err != nil {
		return err
	}
	if err := insertAuditTx(tx, "action_approved", actionID, `{}`, now); err != nil {
		return err
	}
	if source == "chat" {
		systemMsg := approvalSystemMessage(actionID, tool)
		if _, err := tx.Exec(`
			INSERT INTO messages(message_id, thread_id, role, content, created_at)
			VALUES(?, ?, 'system', ?, ?)
		`, newID("msg"), sourceID, systemMsg, now); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	a.emitActionStatusEvent(context.Background(), source, sourceID, "", actionID, protocolv1.ActionStatus_APPROVED, "")
	return nil
}

func (a *App) revokeDevice(deviceID string) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var revokedAt string
	if err := tx.QueryRow(`
		SELECT revoked_at
		FROM devices
		WHERE device_id = ?
	`, deviceID).Scan(&revokedAt); err != nil {
		return err
	}

	if revokedAt != "" {
		return tx.Commit()
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(`
		UPDATE devices
		SET revoked_at = ?
		WHERE device_id = ? AND revoked_at = ''
	`, now, deviceID); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		DELETE FROM auth_tokens
		WHERE device_id = ?
	`, deviceID); err != nil {
		return err
	}
	if err := insertAuditTx(tx, "device_revoked", deviceID, `{"reason":"user_revoked"}`, now); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (a *App) markActionRejected(actionID, reason string) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var status string
	var source string
	var sourceID string
	if err := tx.QueryRow(`
		SELECT status, source, source_id
		FROM proposed_actions
		WHERE action_id = ?
	`, actionID).Scan(&status, &source, &sourceID); err != nil {
		return err
	}
	if status != "PENDING" {
		return fmt.Errorf("action is not pending")
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	payloadBytes, _ := json.Marshal(map[string]string{"reason": reason})

	if _, err := tx.Exec(`
		UPDATE proposed_actions
		SET status = 'REJECTED', rejection_reason = ?
		WHERE action_id = ? AND status = 'PENDING'
	`, reason, actionID); err != nil {
		return err
	}
	if err := insertAuditTx(tx, "action_rejected", actionID, string(payloadBytes), now); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	a.emitActionStatusEvent(context.Background(), source, sourceID, "", actionID, protocolv1.ActionStatus_REJECTED, reason)
	a.maybeResumeTurn(actionID)
	return nil
}

func (a *App) executeApprovedAction(actionID string) error {
	streamCtx := context.Background()

	preflightTx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer preflightTx.Rollback()

	var item approval
	err = preflightTx.QueryRow(`
		SELECT action_id, user_id, source, source_id, tool, args_json, status, idempotency_key
		FROM proposed_actions
		WHERE action_id = ?
	`, actionID).Scan(
		&item.ActionID,
		&item.UserID,
		&item.Source,
		&item.SourceID,
		&item.Tool,
		&item.ArgsJSON,
		&item.Status,
		&item.IdempotencyKey,
	)
	if err != nil {
		return err
	}
	if item.Status != "APPROVED" {
		return fmt.Errorf("action is not approved")
	}

	argsHash := sha256Hex(item.ArgsJSON)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	var existingArgsHash string
	err = preflightTx.QueryRow(`
		SELECT args_hash FROM idempotency
		WHERE owner_id = ? AND tool_name = ? AND key = ?
	`, item.UserID, item.Tool, item.IdempotencyKey).Scan(&existingArgsHash)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		resultHash := sha256Hex("executed:" + item.ActionID)
		if _, err := preflightTx.Exec(`
			INSERT INTO idempotency(owner_id, tool_name, key, args_hash, result_hash, created_at)
			VALUES(?, ?, ?, ?, ?, ?)
		`, item.UserID, item.Tool, item.IdempotencyKey, argsHash, resultHash, now); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if existingArgsHash != argsHash {
			_ = insertAuditTx(preflightTx, "idempotency_conflict", actionID, `{"reason":"args_hash_mismatch"}`, now)
			if _, updateErr := preflightTx.Exec(`
				UPDATE proposed_actions
				SET status = 'REJECTED', rejection_reason = 'idempotency_conflict'
				WHERE action_id = ? AND status = 'APPROVED'
			`, actionID); updateErr != nil {
				return updateErr
			}
			if commitErr := preflightTx.Commit(); commitErr != nil {
				return commitErr
			}
			a.emitActionStatusEvent(streamCtx, item.Source, item.SourceID, "", actionID, protocolv1.ActionStatus_REJECTED, "idempotency_conflict")
			return errIdempotencyConflict
		}
	}
	if err := preflightTx.Commit(); err != nil {
		return err
	}

	executionSystemMsg := fmt.Sprintf("Action %s executed.", item.ActionID)
	actionExecutedAuditPayload := item.ArgsJSON
	if isWebFetchTool(item.Tool) {
		var args agent.FetchArgs
		if err := json.Unmarshal([]byte(item.ArgsJSON), &args); err == nil {
			domain := agent.ExtractDomain(args.URL)
			if domain != "" && item.SourceID != "" {
				if err := a.grantDomain(domain, item.SourceID); err != nil {
					a.logger.Warn("failed to grant domain", "domain", domain, "thread_id", item.SourceID, "error", err)
				} else {
					a.logger.Info("domain granted", "domain", domain, "thread_id", item.SourceID)
				}
			}

			executionID := newID("exec")
			if item.Source == "chat" {
				a.emitToolExecutionStarted(streamCtx, item.SourceID, "", executionID, item.ActionID, item.Tool, args.URL)
			}

			fetchStart := time.Now()
			result, fetchErr := a.webFetcher.Fetch(streamCtx, args)
			fetchDuration := time.Since(fetchStart)
			fetchExitCode := 0
			fetchTruncated := false
			if fetchErr != nil {
				executionSystemMsg = fmt.Sprintf("[web_fetch] error: %v", fetchErr)
				fetchExitCode = 1
			} else {
				var b strings.Builder
				fmt.Fprintf(&b, "[web_fetch] url: %s\n", result.URL)
				if result.FinalURL != "" {
					fmt.Fprintf(&b, "final_url: %s\n", result.FinalURL)
				}
				fmt.Fprintf(&b, "status: %d\ncontent_type: %s\ntruncated: %v\n", result.StatusCode, result.ContentType, result.Truncated)
				b.WriteString(result.Body)
				executionSystemMsg = b.String()
				fetchTruncated = result.Truncated

				if item.Source == "chat" {
					a.emitToolExecutionOutputDelta(streamCtx, item.SourceID, "", executionID, protocolv1.OutputStream_STDOUT, []byte(executionSystemMsg), 0)
				}
			}

			if item.Source == "chat" {
				a.emitToolExecutionFinished(streamCtx, item.SourceID, "", executionID, toolExecutionResult{
					ExitCode:  fetchExitCode,
					Duration:  fetchDuration,
					Truncated: fetchTruncated,
				})
			}

			actionExecutedAuditPayload = fmt.Sprintf(`{"url":%q,"domain":%q,"domain_granted":true}`, args.URL, domain)
		}
	} else if isBashTool(item.Tool) {
		executionID := newID("exec")
		displayCommand := strings.TrimSpace(item.ArgsJSON)
		var parsedArgs bashActionArgs
		if err := json.Unmarshal([]byte(item.ArgsJSON), &parsedArgs); err == nil && strings.TrimSpace(parsedArgs.Command) != "" {
			displayCommand = strings.TrimSpace(parsedArgs.Command)
		}
		if item.Source == "chat" {
			a.emitToolExecutionStarted(streamCtx, item.SourceID, "", executionID, item.ActionID, item.Tool, displayCommand)
		}

		result := executeBashActionStreaming(item.ArgsJSON, func(stream protocolv1.OutputStream, chunk []byte, offset uint64) {
			if item.Source == "chat" {
				a.emitToolExecutionOutputDelta(streamCtx, item.SourceID, "", executionID, stream, chunk, offset)
			}
		})

		if item.Source == "chat" {
			a.emitToolExecutionFinished(streamCtx, item.SourceID, "", executionID, toolExecutionResult{
				ExitCode:  result.ExitCode,
				Duration:  result.Duration,
				TimedOut:  result.TimedOut,
				Truncated: result.Truncated,
			})
		}

		executionSystemMsg = bashExecutionSystemMessage(item.ActionID, result)
		actionExecutedAuditPayload = bashExecutionAuditPayload(item.Tool, result)
	} else if strings.EqualFold(item.Tool, "gmail_create_draft") {
		if a.gmailClient != nil {
			var args agent.GmailCreateDraftArgs
			if err := json.Unmarshal([]byte(item.ArgsJSON), &args); err == nil {
				oauthToken, tokenErr := a.loadOAuthToken(item.UserID, "user", "google")
				if tokenErr != nil {
					executionSystemMsg = fmt.Sprintf("[gmail_create_draft] error: no OAuth token: %v", tokenErr)
				} else if oauthToken.IsExpired() {
					executionSystemMsg = "[gmail_create_draft] error: OAuth token expired"
				} else {
					result, draftErr := a.gmailClient.CreateDraft(streamCtx, oauthToken.AccessToken, args)
					if draftErr != nil {
						executionSystemMsg = fmt.Sprintf("[gmail_create_draft] error: %v", draftErr)
					} else {
						executionSystemMsg = fmt.Sprintf("[gmail_create_draft] Draft created. Draft ID: %s, Message ID: %s", result.DraftID, result.MessageID)
						actionExecutedAuditPayload = fmt.Sprintf(`{"draft_id":%q,"message_id":%q,"to":%q,"subject":%q}`, result.DraftID, result.MessageID, args.To, args.Subject)
					}
				}
			}
		}
	} else if strings.EqualFold(item.Tool, "gmail_send_draft") {
		if a.gmailClient != nil {
			var args agent.GmailSendDraftArgs
			if err := json.Unmarshal([]byte(item.ArgsJSON), &args); err == nil {
				// Send draft uses bot identity per spec.
				oauthToken, tokenErr := a.loadOAuthToken(item.UserID, "bot", "google")
				if tokenErr != nil {
					executionSystemMsg = fmt.Sprintf("[gmail_send_draft] error: no bot OAuth token: %v", tokenErr)
				} else if oauthToken.IsExpired() {
					executionSystemMsg = "[gmail_send_draft] error: bot OAuth token expired"
				} else {
					result, sendErr := a.gmailClient.SendDraft(streamCtx, oauthToken.AccessToken, args)
					if sendErr != nil {
						executionSystemMsg = fmt.Sprintf("[gmail_send_draft] error: %v", sendErr)
					} else {
						executionSystemMsg = fmt.Sprintf("[gmail_send_draft] Draft sent. Message ID: %s, Thread ID: %s", result.MessageID, result.ThreadID)
						actionExecutedAuditPayload = fmt.Sprintf(`{"message_id":%q,"thread_id":%q,"draft_id":%q}`, result.MessageID, result.ThreadID, args.DraftID)
					}
				}
			}
		}
	}

	finalizeTx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer finalizeTx.Rollback()

	res, err := finalizeTx.Exec(`UPDATE proposed_actions SET status = 'EXECUTED' WHERE action_id = ? AND status = 'APPROVED'`, actionID)
	if err != nil {
		return err
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return fmt.Errorf("action is not approved")
	}

	if item.Source == "chat" {
		// Persist as system message for user visibility.
		if _, err := finalizeTx.Exec(`
			INSERT INTO messages(message_id, thread_id, role, content, created_at)
			VALUES(?, ?, 'system', ?, ?)
		`, newID("msg"), item.SourceID, executionSystemMsg, now); err != nil {
			return err
		}
		// Also persist as internal message so the planner sees the result on continuation.
		toolResultMsg := fmt.Sprintf("[tool_result:%s] %s", item.Tool, executionSystemMsg)
		if _, err := finalizeTx.Exec(`
			INSERT INTO messages(message_id, thread_id, role, content, created_at)
			VALUES(?, ?, 'internal', ?, ?)
		`, newID("msg"), item.SourceID, toolResultMsg, now); err != nil {
			return err
		}
	}

	if err := insertAuditTx(finalizeTx, "action_executed", actionID, actionExecutedAuditPayload, now); err != nil {
		return err
	}

	if err := finalizeTx.Commit(); err != nil {
		return err
	}

	a.emitActionStatusEvent(streamCtx, item.Source, item.SourceID, "", actionID, protocolv1.ActionStatus_EXECUTED, "")
	a.logger.Info("action executed", "action_id", actionID, "tool", item.Tool, "source", item.Source)
	return nil
}

// maybeResumeTurn checks if all proposed actions for a turn have been resolved
// (EXECUTED or REJECTED). If so, it triggers a continuation turn so the LLM can
// observe the tool results and continue the planning loop.
func (a *App) maybeResumeTurn(actionID string) {
	var turnID string
	var threadID string
	var source string
	err := a.db.QueryRow(`
		SELECT turn_id, source_id, source
		FROM proposed_actions
		WHERE action_id = ?
	`, actionID).Scan(&turnID, &threadID, &source)
	if err != nil || turnID == "" || source != "chat" {
		return
	}

	var pendingCount int
	err = a.db.QueryRow(`
		SELECT COUNT(*)
		FROM proposed_actions
		WHERE turn_id = ? AND status = 'PENDING'
	`, turnID).Scan(&pendingCount)
	if err != nil || pendingCount > 0 {
		return
	}

	// Atomic guard: prevent double-resumption if multiple actions for the same
	// turn are resolved concurrently.
	if _, loaded := a.resumingTurns.LoadOrStore(turnID, struct{}{}); loaded {
		return
	}

	// Count inline READ tool steps scoped to THIS turn (not the whole thread).
	// Use the turn's first event timestamp to scope internal messages.
	var turnStartedAt string
	err = a.db.QueryRow(`
		SELECT MIN(occurred_at) FROM thread_events WHERE turn_id = ?
	`, turnID).Scan(&turnStartedAt)
	if err != nil || turnStartedAt == "" {
		a.resumingTurns.Delete(turnID)
		return
	}

	var internalToolMessages int
	err = a.db.QueryRow(`
		SELECT COUNT(*)
		FROM messages
		WHERE thread_id = ? AND role = 'internal' AND content LIKE '[tool_call:%'
		AND created_at >= ?
	`, threadID, turnStartedAt).Scan(&internalToolMessages)
	if err != nil {
		internalToolMessages = 0
	}

	// Also count the actions that were just resolved in this turn as steps.
	var resolvedActions int
	err = a.db.QueryRow(`
		SELECT COUNT(*)
		FROM proposed_actions
		WHERE turn_id = ?
	`, turnID).Scan(&resolvedActions)
	if err != nil {
		resolvedActions = 0
	}

	stepsUsed := internalToolMessages + resolvedActions
	if stepsUsed >= maxInlineToolSteps {
		// Budget exhausted — complete the turn without re-planning.
		_, _ = a.appendThreadEvent(context.Background(), &protocolv1.ThreadEvent{
			ThreadId:     threadID,
			TurnId:       turnID,
			Source:       protocolv1.EventSource_SYSTEM,
			ContentTrust: protocolv1.ContentTrust_TRUSTED_SYSTEM,
			Payload: &protocolv1.ThreadEvent_TurnCompleted{TurnCompleted: &protocolv1.TurnCompleted{}},
		})
		a.resumingTurns.Delete(turnID)
		return
	}

	// Load the user message that initiated THIS turn, not a later message.
	var userText string
	err = a.db.QueryRow(`
		SELECT content FROM messages
		WHERE thread_id = ? AND role = 'user'
		AND created_at <= ?
		ORDER BY created_at DESC LIMIT 1
	`, threadID, turnStartedAt).Scan(&userText)
	if err != nil {
		userText = ""
	}

	a.logger.Info("resuming turn after approval", "turn_id", turnID, "thread_id", threadID, "steps_used", stepsUsed)

	go func() {
		defer a.resumingTurns.Delete(turnID)
		_, err := a.executeTurnFromStep(context.Background(), threadID, userText, turnID, protocolv1.TriggerType_CHAT_MESSAGE, stepsUsed, true)
		if err != nil {
			a.logger.Error("turn continuation failed", "turn_id", turnID, "thread_id", threadID, "error", err)
		}
	}()
}

func (a *App) threadExists(threadID string) bool {
	var one int
	err := a.db.QueryRow(`SELECT 1 FROM threads WHERE thread_id = ?`, threadID).Scan(&one)
	return err == nil
}

func (a *App) isDomainGranted(domain, threadID string) bool {
	var one int
	err := a.db.QueryRow(`SELECT 1 FROM domain_grants WHERE domain = ? AND thread_id = ?`, domain, threadID).Scan(&one)
	return err == nil
}

func (a *App) grantDomain(domain, threadID string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := a.db.Exec(`
		INSERT OR IGNORE INTO domain_grants(domain, thread_id, created_at)
		VALUES(?, ?, ?)
	`, domain, threadID, now)
	return err
}

func (a *App) planTurn(ctx context.Context, threadID, userMessage string, step, maxSteps int) (agent.PlanResult, error) {
	history, err := a.loadPlannerHistory(threadID, defaultPlannerHistoryLimit)
	if err != nil {
		return agent.PlanResult{}, fmt.Errorf("load planner history: %w", err)
	}

	plan, err := a.planner.Plan(ctx, agent.PlanRequest{
		ThreadID:    threadID,
		UserMessage: userMessage,
		History:     history,
		Step:        step,
		MaxSteps:    maxSteps,
	})
	if err != nil {
		return agent.PlanResult{}, err
	}

	assistant := strings.TrimSpace(plan.AssistantMessage)
	if assistant == "" {
		assistant = defaultAssistantMessage
	}

	proposed := make([]agent.ProposedAction, 0, len(plan.ProposedActions))
	for _, action := range plan.ProposedActions {
		tool := strings.TrimSpace(action.Tool)
		if tool == "" {
			continue
		}

		args := action.Args
		if !isJSONObject(args) {
			args = defaultActionArgs(threadID, userMessage)
		}

		justification := strings.TrimSpace(action.Justification)
		if justification == "" {
			justification = defaultActionJustification
		}

		riskClass := strings.ToUpper(strings.TrimSpace(action.RiskClass))
		if isBashTool(tool) {
			args = normalizeBashActionArgs(args)
			riskClass = riskClassForBashArgs(args)
		} else if riskClass == "" {
			riskClass = riskClassForTool(tool)
		}

		proposed = append(proposed, agent.ProposedAction{
			Tool:          tool,
			Args:          args,
			Justification: justification,
			RiskClass:     riskClass,
		})
		if len(proposed) >= maxProposedActionsPerTurn {
			break
		}
	}

	return agent.PlanResult{
		AssistantMessage: assistant,
		Thinking:         strings.TrimSpace(plan.Thinking),
		ProposedActions:  proposed,
	}, nil
}

func (a *App) loadPlannerHistory(threadID string, limit int) ([]agent.Message, error) {
	rows, err := a.db.Query(`
		SELECT role, content
		FROM messages
		WHERE thread_id = ? AND role != 'system'
		ORDER BY created_at DESC
		LIMIT ?
	`, threadID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	reversed := make([]agent.Message, 0, limit)
	for rows.Next() {
		var role string
		var content string
		if err := rows.Scan(&role, &content); err != nil {
			return nil, err
		}
		reversed = append(reversed, agent.Message{
			Role:    role,
			Content: content,
		})
	}

	history := make([]agent.Message, 0, len(reversed))
	for i := len(reversed) - 1; i >= 0; i-- {
		history = append(history, reversed[i])
	}

	return history, nil
}

func defaultActionArgs(threadID, userMessage string) json.RawMessage {
	args, _ := json.Marshal(map[string]string{
		"thread_id": threadID,
		"summary":   userMessage,
	})
	return args
}

func riskClassForTool(tool string) string {
	if isBashTool(tool) {
		return "HIGH"
	}

	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "web_search", "web_summarize", "web_fetch", "image_describe":
		return "READ"
	case "gmail_search", "gmail_read", "gmail_get_thread":
		return "READ"
	case "gmail_send_draft":
		return "EXFILTRATION"
	case "gmail_create_draft":
		return "WRITE"
	case "artifact_put", "notes_write":
		return "WRITE"
	default:
		return "HIGH"
	}
}

func prettyActionName(tool string) string {
	trimmed := strings.TrimSpace(tool)
	if trimmed == "" {
		return "Action"
	}
	if isBashTool(trimmed) {
		return "Run Bash"
	}

	display := strings.ReplaceAll(strings.ToLower(trimmed), "_", " ")
	words := strings.Fields(display)
	if len(words) == 0 {
		return "Action"
	}
	for idx, word := range words {
		words[idx] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
}

func isWebFetchTool(tool string) bool {
	return strings.EqualFold(strings.TrimSpace(tool), "web_fetch")
}

func isBashTool(tool string) bool {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "run_bash", "bash_run", "bash", "run_shell":
		return true
	default:
		return false
	}
}

func riskClassForBashArgs(_ json.RawMessage) string {
	return "HIGH"
}

func normalizeBashActionArgs(args json.RawMessage) json.RawMessage {
	var parsed bashActionArgs
	if err := json.Unmarshal(args, &parsed); err != nil {
		return args
	}

	normalized := bashActionArgs{
		Command: strings.TrimSpace(parsed.Command),
		CWD:     strings.TrimSpace(parsed.CWD),
	}
	if parsed.TimeoutMS > 0 {
		timeout := boundedBashExecTimeout(parsed.TimeoutMS)
		normalized.TimeoutMS = int64(timeout / time.Millisecond)
	}

	encoded, err := json.Marshal(normalized)
	if err != nil {
		return args
	}
	return encoded
}

func executeBashAction(argsJSON string) bashExecutionResult {
	return executeBashActionStreaming(argsJSON, nil)
}

func executeBashActionStreaming(argsJSON string, onChunk func(stream protocolv1.OutputStream, chunk []byte, offset uint64)) bashExecutionResult {
	result := bashExecutionResult{ExitCode: -1}

	var args bashActionArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		result.Output = fmt.Sprintf("invalid run_bash args: %v", err)
		return result
	}

	result.Command = strings.TrimSpace(args.Command)
	result.CWD = strings.TrimSpace(args.CWD)
	result.Timeout = boundedBashExecTimeout(args.TimeoutMS)

	if result.Command == "" {
		result.Output = "missing required field: command"
		return result
	}

	output, duration, exitCode, timedOut, truncated := runBashCommandStreamingWithTimeout(result.Command, result.CWD, result.Timeout, onChunk)
	result.Output = output
	result.Duration = duration
	result.ExitCode = exitCode
	result.TimedOut = timedOut
	result.Truncated = truncated
	return result
}

func runBashCommand(command, cwd string) (output string, duration time.Duration, exitCode int, timedOut bool, truncated bool) {
	return runBashCommandStreamingWithTimeout(command, cwd, defaultBashExecTimeout, nil)
}

func runBashCommandStreaming(command, cwd string, onChunk func(stream protocolv1.OutputStream, chunk []byte, offset uint64)) (output string, duration time.Duration, exitCode int, timedOut bool, truncated bool) {
	return runBashCommandStreamingWithTimeout(command, cwd, defaultBashExecTimeout, onChunk)
}

func runBashCommandStreamingWithTimeout(command, cwd string, timeout time.Duration, onChunk func(stream protocolv1.OutputStream, chunk []byte, offset uint64)) (output string, duration time.Duration, exitCode int, timedOut bool, truncated bool) {
	start := time.Now()
	effectiveTimeout := timeout
	if effectiveTimeout <= 0 {
		effectiveTimeout = defaultBashExecTimeout
	}
	if effectiveTimeout > maxBashExecTimeout {
		effectiveTimeout = maxBashExecTimeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), effectiveTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	if cwd != "" {
		cmd.Dir = cwd
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		duration = time.Since(start)
		return appendOutputLine("", fmt.Sprintf("failed to attach stdout pipe: %v", err)), duration, -1, false, false
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = stdoutPipe.Close()
		duration = time.Since(start)
		return appendOutputLine("", fmt.Sprintf("failed to attach stderr pipe: %v", err)), duration, -1, false, false
	}

	if err := cmd.Start(); err != nil {
		_ = stdoutPipe.Close()
		_ = stderrPipe.Close()
		duration = time.Since(start)
		return appendOutputLine("", fmt.Sprintf("failed to start bash command: %v", err)), duration, -1, false, false
	}

	type outputChunk struct {
		stream protocolv1.OutputStream
		data   []byte
	}
	chunks := make(chan outputChunk, 64)
	var readerWG sync.WaitGroup
	readerWG.Add(2)
	readPipe := func(stream protocolv1.OutputStream, pipe io.ReadCloser) {
		defer readerWG.Done()
		defer pipe.Close()

		buffer := make([]byte, 1024)
		for {
			n, readErr := pipe.Read(buffer)
			if n > 0 {
				copied := make([]byte, n)
				copy(copied, buffer[:n])
				chunks <- outputChunk{stream: stream, data: copied}
			}
			if readErr != nil {
				if errors.Is(readErr, io.EOF) {
					return
				}
				return
			}
		}
	}
	go readPipe(protocolv1.OutputStream_STDOUT, stdoutPipe)
	go readPipe(protocolv1.OutputStream_STDERR, stderrPipe)
	go func() {
		readerWG.Wait()
		close(chunks)
	}()

	bounded := newBoundedOutputBuffer(maxBashOutputBytes)
	var offset uint64
	for chunk := range chunks {
		_, _ = bounded.Write(chunk.data)
		if onChunk != nil {
			onChunk(chunk.stream, chunk.data, offset)
		}
		offset += uint64(len(chunk.data))
	}

	err = cmd.Wait()
	duration = time.Since(start)

	output = strings.TrimSpace(bounded.String())
	exitCode = 0
	timedOut = false
	truncated = bounded.Truncated()

	switch {
	case err == nil:
		// no-op
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		timedOut = true
		exitCode = -1
		output = appendOutputLine(output, fmt.Sprintf("command timed out after %s", effectiveTimeout))
	default:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
			output = appendOutputLine(output, fmt.Sprintf("failed to execute bash command: %v", err))
		}
	}

	if output == "" {
		output = "(no output)"
	}
	return output, duration, exitCode, timedOut, truncated
}

func appendOutputLine(existing, next string) string {
	existing = strings.TrimSpace(existing)
	next = strings.TrimSpace(next)
	switch {
	case existing == "":
		return next
	case next == "":
		return existing
	default:
		return existing + "\n" + next
	}
}

func approvalSystemMessage(actionID, tool string) string {
	lines := []string{
		fmt.Sprintf("Action %s approved.", actionID),
		fmt.Sprintf("Tool: %s", prettyActionName(tool)),
		"Status: Awaiting execution",
	}
	return strings.Join(lines, "\n")
}

func bashExecutionSystemMessage(actionID string, result bashExecutionResult) string {
	lines := []string{
		fmt.Sprintf("Action %s executed.", actionID),
		fmt.Sprintf("Command: %s", result.Command),
		fmt.Sprintf("Timeout: %dms", result.Timeout.Milliseconds()),
		fmt.Sprintf("Exit code: %d", result.ExitCode),
		fmt.Sprintf("Duration: %dms", result.Duration.Milliseconds()),
	}
	if result.CWD != "" {
		lines = append(lines, fmt.Sprintf("CWD: %s", result.CWD))
	}
	if result.TimedOut {
		lines = append(lines, "Timed out: true")
	}
	if result.Truncated {
		lines = append(lines, "Output truncated: true")
	}
	lines = append(lines, "Output:", result.Output)

	msg := strings.Join(lines, "\n")
	if len(msg) > maxBashSystemMessageChars {
		return msg[:maxBashSystemMessageChars] + "\n...[truncated]"
	}
	return msg
}

func bashExecutionAuditPayload(tool string, result bashExecutionResult) string {
	payload := map[string]any{
		"tool":             tool,
		"command":          result.Command,
		"cwd":              result.CWD,
		"timeout_ms":       result.Timeout.Milliseconds(),
		"duration_ms":      result.Duration.Milliseconds(),
		"exit_code":        result.ExitCode,
		"timed_out":        result.TimedOut,
		"output_truncated": result.Truncated,
		"output":           result.Output,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return `{}`
	}
	return string(payloadBytes)
}

func boundedBashExecTimeout(timeoutMS int64) time.Duration {
	if timeoutMS <= 0 {
		return defaultBashExecTimeout
	}

	maxTimeoutMS := int64(maxBashExecTimeout / time.Millisecond)
	if timeoutMS > maxTimeoutMS {
		return maxBashExecTimeout
	}

	return time.Duration(timeoutMS) * time.Millisecond
}

type boundedOutputBuffer struct {
	limit     int
	total     int
	builder   strings.Builder
	truncated bool
}

func newBoundedOutputBuffer(limit int) *boundedOutputBuffer {
	if limit <= 0 {
		limit = 1
	}
	return &boundedOutputBuffer{limit: limit}
}

func (b *boundedOutputBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.total < b.limit {
		remaining := b.limit - b.total
		if n > remaining {
			_, _ = b.builder.Write(p[:remaining])
			b.truncated = true
		} else {
			_, _ = b.builder.Write(p)
		}
	} else {
		b.truncated = true
	}
	b.total += n
	if b.total > b.limit {
		b.truncated = true
	}
	return n, nil
}

func (b *boundedOutputBuffer) String() string {
	return b.builder.String()
}

func (b *boundedOutputBuffer) Truncated() bool {
	return b.truncated
}

func isJSONObject(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	if !json.Valid(raw) {
		return false
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return false
	}
	_, ok := decoded.(map[string]any)
	return ok
}

func clientIP(remoteAddr string) string {
	remoteAddr = strings.TrimSpace(remoteAddr)
	if remoteAddr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

func (a *App) runActionExecutor() {
	ticker := time.NewTicker(a.actionExecutorInterval)
	defer func() {
		ticker.Stop()
		close(a.doneCh)
	}()

	a.processActionQueueOnce()

	for {
		select {
		case <-a.stopCh:
			return
		case <-ticker.C:
			a.processActionQueueOnce()
		}
	}
}

func (a *App) processActionQueueOnce() {
	now := time.Now().UTC()
	if err := a.expirePendingActions(now); err != nil {
		a.logger.Error("action executor failed to expire pending actions", "error", err)
	}

	actionIDs, err := a.listApprovedActionIDs()
	if err != nil {
		a.logger.Error("action executor failed to list approved actions", "error", err)
		return
	}

	for _, actionID := range actionIDs {
		if err := a.executeApprovedAction(actionID); err != nil {
			if errors.Is(err, errIdempotencyConflict) {
				a.logger.Warn("action executor idempotency conflict", "action_id", actionID)
				a.maybeResumeTurn(actionID)
				continue
			}
			a.logger.Error("action executor failed to execute action", "action_id", actionID, "error", err)
			continue
		}
		a.maybeResumeTurn(actionID)
	}
}

func (a *App) listApprovedActionIDs() ([]string, error) {
	rows, err := a.db.Query(`
		SELECT action_id
		FROM proposed_actions
		WHERE status = 'APPROVED'
		ORDER BY created_at ASC
		LIMIT 50
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var actionID string
		if err := rows.Scan(&actionID); err != nil {
			return nil, err
		}
		ids = append(ids, actionID)
	}
	return ids, nil
}

func (a *App) expirePendingActions(now time.Time) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`
		SELECT action_id, source, source_id, expires_at
		FROM proposed_actions
		WHERE status = 'PENDING'
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type expiredAction struct {
		actionID string
		source   string
		sourceID string
	}
	expiredActions := make([]expiredAction, 0)
	for rows.Next() {
		var actionID string
		var source string
		var sourceID string
		var expiresAtRaw string
		if err := rows.Scan(&actionID, &source, &sourceID, &expiresAtRaw); err != nil {
			return err
		}
		expiresAt, err := parseTimestamp(expiresAtRaw)
		if err != nil {
			return fmt.Errorf("parse expires_at for action %s: %w", actionID, err)
		}
		if !expiresAt.After(now) {
			expiredActions = append(expiredActions, expiredAction{
				actionID: actionID,
				source:   source,
				sourceID: sourceID,
			})
		}
	}

	if len(expiredActions) == 0 {
		return tx.Commit()
	}

	nowStr := now.Format(time.RFC3339Nano)
	for _, action := range expiredActions {
		res, err := tx.Exec(`
			UPDATE proposed_actions
			SET status = 'REJECTED', rejection_reason = 'expired'
			WHERE action_id = ? AND status = 'PENDING'
		`, action.actionID)
		if err != nil {
			return err
		}
		rowsAffected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if rowsAffected == 0 {
			continue
		}
		if err := insertAuditTx(tx, "action_expired", action.actionID, `{"reason":"expired"}`, nowStr); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	for _, action := range expiredActions {
		a.emitActionStatusEvent(context.Background(), action.source, action.sourceID, "", action.actionID, protocolv1.ActionStatus_REJECTED, "expired")
	}
	a.logger.Info("pending actions expired", "count", len(expiredActions))
	return nil
}

func (a *App) activeDeviceCount() (int, error) {
	var count int
	err := a.db.QueryRow(`SELECT COUNT(*) FROM devices WHERE revoked_at = ''`).Scan(&count)
	return count, err
}

func (a *App) deviceIDForToken(rawToken string) (string, error) {
	tokenID, err := tokenIDFromOpaqueToken(rawToken)
	if err != nil {
		return "", err
	}
	var deviceID string
	err = a.db.QueryRow(`
		SELECT device_id
		FROM auth_tokens
		WHERE token_id = ?
	`, tokenID).Scan(&deviceID)
	if err != nil {
		return "", err
	}
	return deviceID, nil
}

func (a *App) hashToken(token string) string {
	mac := hmac.New(sha256.New, a.tokenHMACKey)
	_, _ = mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

func (a *App) hashPairingCode(code string) string {
	return a.hashToken("pairing:" + strings.ToUpper(strings.TrimSpace(code)))
}

func (a *App) validateAndTouchToken(rawToken string) error {
	tokenID, err := tokenIDFromOpaqueToken(rawToken)
	if err != nil {
		return err
	}

	var storedHash string
	var expiresAtRaw string
	var lastUsedAtRaw string
	var revokedAtRaw string
	err = a.db.QueryRow(`
		SELECT t.token_hash, t.expires_at, t.last_used_at, d.revoked_at
		FROM auth_tokens t
		JOIN devices d ON d.device_id = t.device_id
		WHERE t.token_id = ?
	`, tokenID).Scan(&storedHash, &expiresAtRaw, &lastUsedAtRaw, &revokedAtRaw)
	if err != nil {
		return err
	}

	computedHash := a.hashToken(rawToken)
	if subtle.ConstantTimeCompare([]byte(storedHash), []byte(computedHash)) != 1 {
		return errors.New("token mismatch")
	}
	if revokedAtRaw != "" {
		return errors.New("device revoked")
	}

	expiresAt, err := parseTimestamp(expiresAtRaw)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if !expiresAt.After(now) {
		return errors.New("token expired")
	}

	shouldUpdateLastUsed := true
	if lastUsedAtRaw != "" {
		lastUsedAt, parseErr := parseTimestamp(lastUsedAtRaw)
		if parseErr == nil && now.Sub(lastUsedAt) < lastUsedUpdateInterval {
			shouldUpdateLastUsed = false
		}
	}

	newExpiresAt := expiresAt
	if expiresAt.Sub(now) <= defaultTokenRenewWindow {
		newExpiresAt = now.Add(defaultTokenTTL)
		shouldUpdateLastUsed = true
	}

	if shouldUpdateLastUsed {
		_, _ = a.db.Exec(`
			UPDATE auth_tokens
			SET expires_at = ?, last_used_at = ?
			WHERE token_id = ?
		`, newExpiresAt.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), tokenID)
	}

	return nil
}

func insertAuditTx(tx *sql.Tx, eventType, entityID, payload, createdAt string) error {
	_, err := tx.Exec(`
		INSERT INTO audit_log(entry_id, event_type, entity_id, payload_json, created_at)
		VALUES(?, ?, ?, ?, ?)
	`, newID("aud"), eventType, entityID, payload, createdAt)
	return err
}

func migrate(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS devices(
			device_id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			name TEXT NOT NULL,
			public_key TEXT NOT NULL,
			revoked_at TEXT NOT NULL,
			created_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS auth_tokens(
			token_id TEXT PRIMARY KEY,
			device_id TEXT NOT NULL,
			token_hash TEXT NOT NULL UNIQUE,
			expires_at TEXT NOT NULL,
			last_used_at TEXT NOT NULL,
			created_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS pairing_codes(
			code_hash TEXT PRIMARY KEY,
			expires_at TEXT NOT NULL,
			consumed_at TEXT NOT NULL,
			created_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS threads(
			thread_id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			channel TEXT NOT NULL,
			created_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS messages(
			message_id TEXT PRIMARY KEY,
			thread_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS proposed_actions(
			action_id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			source TEXT NOT NULL,
			source_id TEXT NOT NULL,
			tool TEXT NOT NULL,
			args_json TEXT NOT NULL,
			risk_class TEXT NOT NULL,
			justification TEXT NOT NULL,
			idempotency_key TEXT NOT NULL,
			status TEXT NOT NULL,
			rejection_reason TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			created_at TEXT NOT NULL,
			turn_id TEXT NOT NULL DEFAULT '',
			UNIQUE(user_id, tool, idempotency_key)
		);`,
		`CREATE TABLE IF NOT EXISTS idempotency(
			owner_id TEXT NOT NULL,
			tool_name TEXT NOT NULL,
			key TEXT NOT NULL,
			args_hash TEXT NOT NULL,
			result_hash TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY(owner_id, tool_name, key)
		);`,
		`CREATE TABLE IF NOT EXISTS audit_log(
			entry_id TEXT PRIMARY KEY,
			event_type TEXT NOT NULL,
			entity_id TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			created_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS thread_events(
			event_id TEXT PRIMARY KEY,
			thread_id TEXT NOT NULL,
			job_id TEXT NOT NULL,
			turn_id TEXT NOT NULL,
			sequence INTEGER NOT NULL,
			occurred_at TEXT NOT NULL,
			event_blob BLOB NOT NULL,
			UNIQUE(thread_id, sequence)
		);`,
		`CREATE TABLE IF NOT EXISTS domain_grants(
			domain TEXT NOT NULL,
			thread_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY(domain, thread_id)
		);`,
		`CREATE TABLE IF NOT EXISTS oauth_tokens(
			user_id TEXT NOT NULL,
			identity TEXT NOT NULL,
			provider TEXT NOT NULL,
			token_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY(user_id, identity, provider)
		);`,
		`CREATE TABLE IF NOT EXISTS cached_images(
			url_hash TEXT PRIMARY KEY,
			original_url TEXT NOT NULL,
			content_type TEXT NOT NULL,
			data BLOB NOT NULL,
			created_at TEXT NOT NULL
		);`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}

	// Additive column migrations for existing databases.
	addColumns := []struct{ table, column, colDef string }{
		{"proposed_actions", "turn_id", "TEXT NOT NULL DEFAULT ''"},
	}
	for _, ac := range addColumns {
		_, _ = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", ac.table, ac.column, ac.colDef))
	}

	return nil
}

func (a *App) storeOAuthToken(userID, identity, provider string, token agent.OAuthToken) error {
	tokenJSON, err := json.Marshal(token)
	if err != nil {
		return fmt.Errorf("marshal oauth token: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = a.db.Exec(`
		INSERT INTO oauth_tokens(user_id, identity, provider, token_json, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, identity, provider) DO UPDATE SET
			token_json = excluded.token_json,
			updated_at = excluded.updated_at
	`, userID, identity, provider, string(tokenJSON), now, now)
	return err
}

func (a *App) loadOAuthToken(userID, identity, provider string) (agent.OAuthToken, error) {
	var tokenJSON string
	err := a.db.QueryRow(`
		SELECT token_json FROM oauth_tokens
		WHERE user_id = ? AND identity = ? AND provider = ?
	`, userID, identity, provider).Scan(&tokenJSON)
	if err != nil {
		return agent.OAuthToken{}, err
	}
	var token agent.OAuthToken
	if err := json.Unmarshal([]byte(tokenJSON), &token); err != nil {
		return agent.OAuthToken{}, fmt.Errorf("unmarshal oauth token: %w", err)
	}
	return token, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeJSON(body io.ReadCloser, out any) error {
	defer body.Close()
	if err := json.NewDecoder(body).Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return nil
}

func newID(prefix string) string {
	var b [8]byte
	_, err := rand.Read(b[:])
	if err != nil {
		now := time.Now().UnixNano()
		return fmt.Sprintf("%s_%d", prefix, now)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

func newPairingCode() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	value := binary.BigEndian.Uint32(b[:]) % 100000000
	return fmt.Sprintf("%08d", value), nil
}

func newOpaqueToken() (tokenID string, tokenValue string, err error) {
	id := make([]byte, 9)
	secret := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		return "", "", err
	}
	if _, err := rand.Read(secret); err != nil {
		return "", "", err
	}

	tokenID = base64.RawURLEncoding.EncodeToString(id)
	tokenSecret := base64.RawURLEncoding.EncodeToString(secret)
	tokenValue = "pnr_" + tokenID + "." + tokenSecret
	return tokenID, tokenValue, nil
}

func tokenIDFromOpaqueToken(rawToken string) (string, error) {
	if !strings.HasPrefix(rawToken, "pnr_") {
		return "", errors.New("invalid token format")
	}
	rest := strings.TrimPrefix(rawToken, "pnr_")
	parts := strings.Split(rest, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", errors.New("invalid token format")
	}
	return parts[0], nil
}

func bearerTokenFromHeader(authz string) string {
	if !strings.HasPrefix(authz, "Bearer ") {
		return ""
	}
	token := strings.TrimSpace(strings.TrimPrefix(authz, "Bearer "))
	if token == "" {
		return ""
	}
	return token
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func parseTimestamp(raw string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err == nil {
		return t, nil
	}
	t, err = time.Parse(time.RFC3339, raw)
	if err == nil {
		return t, nil
	}
	return time.Time{}, err
}
