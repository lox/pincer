package server

import (
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
	"strings"
	"sync"
	"time"

	charmLog "github.com/charmbracelet/log"
	"github.com/lox/pincer/internal/agent"
	_ "modernc.org/sqlite"
)

const (
	defaultOwnerID                    = "owner-dev"
	defaultTokenHMACKey               = "pincer-dev-token-hmac-key-change-me"
	defaultPrimaryModel               = "anthropic/claude-opus-4.6"
	defaultAssistantMessage           = "I prepared a proposed external action. Review it in Approvals before execution."
	defaultActionTool                 = "demo_external_notify"
	defaultActionRiskClass            = "EXFILTRATION"
	defaultActionJustification        = "User requested external follow-up"
	defaultActionExpiry               = 24 * time.Hour
	defaultPlannerHistoryLimit        = 12
	maxProposedActionsPerTurn         = 3
	defaultActionExecutorPollInterval = 250 * time.Millisecond
	defaultTokenTTL                   = 30 * 24 * time.Hour
	defaultTokenRenewWindow           = 7 * 24 * time.Hour
	defaultPairingCodeTTL             = 10 * time.Minute
	lastUsedUpdateInterval            = time.Hour
)

var errIdempotencyConflict = errors.New("idempotency conflict")

type AppConfig struct {
	DBPath                  string
	TokenHMACKey            string
	OpenRouterAPIKey        string
	OpenRouterBaseURL       string
	ModelPrimary            string
	ModelFallback           string
	Logger                  *charmLog.Logger
	Planner                 agent.Planner
	ActionExecutorInterval  time.Duration
	DisableBackgroundWorker bool
}

type App struct {
	db                     *sql.DB
	tokenHMACKey           []byte
	logger                 *charmLog.Logger
	planner                agent.Planner
	ownerID                string
	stopCh                 chan struct{}
	doneCh                 chan struct{}
	closeOnce              sync.Once
	actionExecutorInterval time.Duration
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

	app := &App{
		db:                     db,
		tokenHMACKey:           []byte(tokenHMACKey),
		logger:                 logger,
		planner:                planner,
		ownerID:                defaultOwnerID,
		stopCh:                 make(chan struct{}),
		doneCh:                 make(chan struct{}),
		actionExecutorInterval: interval,
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
	mux.HandleFunc("/v1/pairing/code", a.handlePairingCode)
	mux.HandleFunc("/v1/pairing/bind", a.handlePairingBind)
	mux.HandleFunc("/v1/chat/threads", a.handleThreads)
	mux.HandleFunc("/v1/chat/threads/", a.handleThreadSubroutes)
	mux.HandleFunc("/v1/devices", a.handleDevices)
	mux.HandleFunc("/v1/devices/", a.handleDeviceSubroutes)
	mux.HandleFunc("/v1/approvals", a.handleApprovals)
	mux.HandleFunc("/v1/approvals/", a.handleApprovalSubroutes)
	mux.HandleFunc("/v1/audit", a.handleAudit)

	return a.loggingMiddleware(a.authMiddleware(mux))
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

func (a *App) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/pairing/") {
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

func (a *App) handlePairingCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	activeDevices, err := a.activeDeviceCount()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to check pairing state"})
		return
	}

	if activeDevices > 0 {
		rawToken := bearerTokenFromHeader(r.Header.Get("Authorization"))
		if rawToken == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		if err := a.validateAndTouchToken(rawToken); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
	}

	code, err := newPairingCode()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate pairing code"})
		return
	}
	expiresAt := time.Now().UTC().Add(defaultPairingCodeTTL)

	if _, err := a.db.Exec(`
		INSERT INTO pairing_codes(code_hash, expires_at, consumed_at, created_at)
		VALUES(?, ?, '', ?)
	`, a.hashPairingCode(code), expiresAt.Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to persist pairing code"})
		return
	}

	a.logger.Info(
		"pairing code issued",
		"expires_at", expiresAt.Format(time.RFC3339Nano),
		"active_devices", activeDevices,
	)

	writeJSON(w, http.StatusCreated, createPairingCodeResponse{
		Code:      code,
		ExpiresAt: expiresAt.Format(time.RFC3339Nano),
	})
}

func (a *App) handlePairingBind(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	var req bindPairingRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	code := strings.TrimSpace(req.Code)
	if code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "code is required"})
		return
	}
	deviceName := strings.TrimSpace(req.DeviceName)
	if deviceName == "" {
		deviceName = "Pincer Device"
	}

	tokenID, tokenValue, err := newOpaqueToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate token"})
		return
	}

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)
	expiresAt := now.Add(defaultTokenTTL)
	expiresAtStr := expiresAt.Format(time.RFC3339Nano)
	deviceID := newID("dev")

	tx, err := a.db.Begin()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to begin transaction"})
		return
	}
	defer tx.Rollback()

	var pairingExpiresAtRaw string
	var consumedAt string
	err = tx.QueryRow(`
		SELECT expires_at, consumed_at
		FROM pairing_codes
		WHERE code_hash = ?
	`, a.hashPairingCode(code)).Scan(&pairingExpiresAtRaw, &consumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid pairing code"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to validate pairing code"})
		return
	}
	if consumedAt != "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid pairing code"})
		return
	}
	pairingExpiresAt, err := parseTimestamp(pairingExpiresAtRaw)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to parse pairing code expiry"})
		return
	}
	if !pairingExpiresAt.After(now) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid pairing code"})
		return
	}

	if _, err := tx.Exec(`
		UPDATE pairing_codes
		SET consumed_at = ?
		WHERE code_hash = ? AND consumed_at = ''
	`, nowStr, a.hashPairingCode(code)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to consume pairing code"})
		return
	}

	// Phase 1 default: one active device. Revoke existing devices and remove their tokens.
	if _, err := tx.Exec(`
		UPDATE devices
		SET revoked_at = ?
		WHERE revoked_at = ''
	`, nowStr); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to revoke existing devices"})
		return
	}
	if _, err := tx.Exec(`DELETE FROM auth_tokens`); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to invalidate existing tokens"})
		return
	}

	if _, err := tx.Exec(`
		INSERT INTO devices(device_id, user_id, name, public_key, revoked_at, created_at)
		VALUES(?, ?, ?, ?, '', ?)
	`, deviceID, a.ownerID, deviceName, req.PublicKey, nowStr); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create device"})
		return
	}
	if _, err := tx.Exec(`
		INSERT INTO auth_tokens(token_id, device_id, token_hash, expires_at, last_used_at, created_at)
		VALUES(?, ?, ?, ?, ?, ?)
	`, tokenID, deviceID, a.hashToken(tokenValue), expiresAtStr, nowStr, nowStr); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create token"})
		return
	}

	if err := insertAuditTx(tx, "device_paired", deviceID, fmt.Sprintf(`{"name":%q}`, deviceName), nowStr); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to write audit event"})
		return
	}

	if err := tx.Commit(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to commit transaction"})
		return
	}

	a.logger.Info("device paired", "device_id", deviceID, "device_name", deviceName, "expires_at", expiresAtStr)

	writeJSON(w, http.StatusCreated, bindPairingResponse{
		DeviceID:  deviceID,
		Token:     tokenValue,
		ExpiresAt: expiresAtStr,
	})
}

func (a *App) handleThreads(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	threadID := newID("thr")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := a.db.Exec(`
		INSERT INTO threads(thread_id, user_id, channel, created_at)
		VALUES(?, ?, 'ios', ?)
	`, threadID, a.ownerID, now); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create thread"})
		return
	}

	a.logger.Info("thread created", "thread_id", threadID, "channel", "ios")

	writeJSON(w, http.StatusCreated, threadResponse{ThreadID: threadID})
}

func (a *App) handleThreadSubroutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/chat/threads/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[1] != "messages" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	threadID := parts[0]
	if threadID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing thread id"})
		return
	}

	switch r.Method {
	case http.MethodPost:
		a.handlePostMessage(w, r, threadID)
	case http.MethodGet:
		a.handleListMessages(w, threadID)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (a *App) handlePostMessage(w http.ResponseWriter, r *http.Request, threadID string) {
	var req createMessageRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "content is required"})
		return
	}
	if !a.threadExists(threadID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "thread not found"})
		return
	}

	plan, err := a.planTurn(r.Context(), threadID, req.Content)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to plan response"})
		return
	}

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)
	expiresAt := now.Add(defaultActionExpiry).Format(time.RFC3339Nano)

	tx, err := a.db.Begin()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to begin transaction"})
		return
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		INSERT INTO messages(message_id, thread_id, role, content, created_at)
		VALUES(?, ?, 'user', ?, ?)
	`, newID("msg"), threadID, req.Content, nowStr); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to insert user message"})
		return
	}
	if _, err := tx.Exec(`
		INSERT INTO messages(message_id, thread_id, role, content, created_at)
		VALUES(?, ?, 'assistant', ?, ?)
	`, newID("msg"), threadID, plan.AssistantMessage, nowStr); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to insert assistant message"})
		return
	}

	firstActionID := ""
	for _, proposed := range plan.ProposedActions {
		actionID := newID("act")
		idempotencyKey := newID("idem")
		argsJSON := string(proposed.Args)
		if _, err := tx.Exec(`
			INSERT INTO proposed_actions(
				action_id, user_id, source, source_id, tool, args_json, risk_class,
				justification, idempotency_key, status, rejection_reason, expires_at, created_at
			) VALUES(?, ?, 'chat', ?, ?, ?, ?,
				?, ?, 'PENDING', '', ?, ?)
		`, actionID, a.ownerID, threadID, proposed.Tool, argsJSON, proposed.RiskClass, proposed.Justification, idempotencyKey, expiresAt, nowStr); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to insert action"})
			return
		}
		if err := insertAuditTx(tx, "action_proposed", actionID, argsJSON, nowStr); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to insert audit"})
			return
		}
		if firstActionID == "" {
			firstActionID = actionID
		}
	}

	if err := tx.Commit(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to commit transaction"})
		return
	}

	a.logger.Info(
		"chat turn planned",
		"thread_id", threadID,
		"proposed_actions", len(plan.ProposedActions),
	)

	writeJSON(w, http.StatusCreated, createMessageResponse{
		AssistantMessage: plan.AssistantMessage,
		ActionID:         firstActionID,
	})
}

func (a *App) handleListMessages(w http.ResponseWriter, threadID string) {
	rows, err := a.db.Query(`
		SELECT message_id, thread_id, role, content, created_at
		FROM messages
		WHERE thread_id = ?
		ORDER BY created_at ASC
	`, threadID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list messages"})
		return
	}
	defer rows.Close()

	items := make([]message, 0)
	for rows.Next() {
		var m message
		if err := rows.Scan(&m.MessageID, &m.ThreadID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to scan message"})
			return
		}
		items = append(items, m)
	}

	writeJSON(w, http.StatusOK, messagesResponse{Items: items})
}

func (a *App) handleApprovals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "pending"
	}
	status = strings.ToUpper(status)

	rows, err := a.db.Query(`
		SELECT action_id, source, source_id, tool, status, risk_class, idempotency_key, justification, created_at, expires_at, rejection_reason
		FROM proposed_actions
		WHERE status = ?
		ORDER BY created_at ASC
	`, status)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list approvals"})
		return
	}
	defer rows.Close()

	items := make([]approval, 0)
	for rows.Next() {
		var aItem approval
		if err := rows.Scan(
			&aItem.ActionID,
			&aItem.Source,
			&aItem.SourceID,
			&aItem.Tool,
			&aItem.Status,
			&aItem.RiskClass,
			&aItem.IdempotencyKey,
			&aItem.Justification,
			&aItem.CreatedAt,
			&aItem.ExpiresAt,
			&aItem.RejectionReason,
		); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to scan approval"})
			return
		}
		items = append(items, aItem)
	}

	writeJSON(w, http.StatusOK, approvalsResponse{Items: items})
}

func (a *App) handleDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	currentDeviceID := ""
	rawToken := bearerTokenFromHeader(r.Header.Get("Authorization"))
	if rawToken != "" {
		id, err := a.deviceIDForToken(rawToken)
		if err == nil {
			currentDeviceID = id
		}
	}

	rows, err := a.db.Query(`
		SELECT device_id, name, revoked_at, created_at
		FROM devices
		ORDER BY created_at DESC
	`)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list devices"})
		return
	}
	defer rows.Close()

	items := make([]device, 0)
	for rows.Next() {
		var item device
		if err := rows.Scan(&item.DeviceID, &item.Name, &item.RevokedAt, &item.CreatedAt); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to scan device"})
			return
		}
		item.IsCurrent = item.DeviceID == currentDeviceID
		items = append(items, item)
	}

	writeJSON(w, http.StatusOK, devicesResponse{Items: items})
}

func (a *App) handleDeviceSubroutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/devices/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[1] != "revoke" || r.Method != http.MethodPost {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	deviceID := strings.TrimSpace(parts[0])
	if deviceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing device id"})
		return
	}

	if err := a.revokeDevice(deviceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "device not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to revoke device"})
		return
	}

	a.logger.Info("device revoked", "device_id", deviceID)

	writeJSON(w, http.StatusOK, map[string]string{"device_id": deviceID, "status": "REVOKED"})
}

func (a *App) handleApprovalSubroutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/approvals/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || r.Method != http.MethodPost {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	actionID := parts[0]
	if actionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing action id"})
		return
	}

	switch parts[1] {
	case "approve":
		if err := a.markActionApproved(actionID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "action not found"})
				return
			}
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		a.logger.Info("action approved", "action_id", actionID)
		writeJSON(w, http.StatusOK, map[string]string{"action_id": actionID, "status": "APPROVED"})
	case "reject":
		var req rejectActionRequest
		if err := decodeJSON(r.Body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		reason := strings.TrimSpace(req.Reason)
		if reason == "" {
			reason = "rejected_by_user"
		}
		if err := a.markActionRejected(actionID, reason); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "action not found"})
				return
			}
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		a.logger.Info("action rejected", "action_id", actionID, "reason", reason)
		writeJSON(w, http.StatusOK, map[string]string{"action_id": actionID, "status": "REJECTED"})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func (a *App) handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	rows, err := a.db.Query(`
		SELECT entry_id, event_type, entity_id, payload_json, created_at
		FROM audit_log
		ORDER BY created_at ASC
	`)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list audit"})
		return
	}
	defer rows.Close()

	items := make([]auditEvent, 0)
	for rows.Next() {
		var e auditEvent
		if err := rows.Scan(&e.EntryID, &e.EventType, &e.EntityID, &e.Payload, &e.CreatedAt); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to scan audit"})
			return
		}
		items = append(items, e)
	}
	writeJSON(w, http.StatusOK, auditResponse{Items: items})
}

func (a *App) markActionApproved(actionID string) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var status string
	var expiresAtRaw string
	err = tx.QueryRow(`SELECT status, expires_at FROM proposed_actions WHERE action_id = ?`, actionID).Scan(&status, &expiresAtRaw)
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
		return fmt.Errorf("action is expired")
	}

	if _, err := tx.Exec(`UPDATE proposed_actions SET status = 'APPROVED' WHERE action_id = ?`, actionID); err != nil {
		return err
	}
	if err := insertAuditTx(tx, "action_approved", actionID, `{}`, now); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
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
	if err := tx.QueryRow(`SELECT status FROM proposed_actions WHERE action_id = ?`, actionID).Scan(&status); err != nil {
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
	return nil
}

func (a *App) executeApprovedAction(actionID string) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var item approval
	err = tx.QueryRow(`
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
	err = tx.QueryRow(`
		SELECT args_hash FROM idempotency
		WHERE owner_id = ? AND tool_name = ? AND key = ?
	`, item.UserID, item.Tool, item.IdempotencyKey).Scan(&existingArgsHash)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		resultHash := sha256Hex("executed:" + item.ActionID)
		if _, err := tx.Exec(`
			INSERT INTO idempotency(owner_id, tool_name, key, args_hash, result_hash, created_at)
			VALUES(?, ?, ?, ?, ?, ?)
		`, item.UserID, item.Tool, item.IdempotencyKey, argsHash, resultHash, now); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if existingArgsHash != argsHash {
			_ = insertAuditTx(tx, "idempotency_conflict", actionID, `{"reason":"args_hash_mismatch"}`, now)
			if _, updateErr := tx.Exec(`
				UPDATE proposed_actions
				SET status = 'REJECTED', rejection_reason = 'idempotency_conflict'
				WHERE action_id = ? AND status = 'APPROVED'
			`, actionID); updateErr != nil {
				return updateErr
			}
			if commitErr := tx.Commit(); commitErr != nil {
				return commitErr
			}
			return errIdempotencyConflict
		}
	}

	if _, err := tx.Exec(`UPDATE proposed_actions SET status = 'EXECUTED' WHERE action_id = ?`, actionID); err != nil {
		return err
	}

	if item.Source == "chat" {
		systemMsg := fmt.Sprintf("Action %s executed.", item.ActionID)
		if _, err := tx.Exec(`
			INSERT INTO messages(message_id, thread_id, role, content, created_at)
			VALUES(?, ?, 'system', ?, ?)
		`, newID("msg"), item.SourceID, systemMsg, now); err != nil {
			return err
		}
	}

	if err := insertAuditTx(tx, "action_executed", actionID, item.ArgsJSON, now); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	a.logger.Info("action executed", "action_id", actionID, "tool", item.Tool, "source", item.Source)
	return nil
}

func (a *App) threadExists(threadID string) bool {
	var one int
	err := a.db.QueryRow(`SELECT 1 FROM threads WHERE thread_id = ?`, threadID).Scan(&one)
	return err == nil
}

func (a *App) planTurn(ctx context.Context, threadID, userMessage string) (agent.PlanResult, error) {
	history, err := a.loadPlannerHistory(threadID, defaultPlannerHistoryLimit)
	if err != nil {
		return agent.PlanResult{}, fmt.Errorf("load planner history: %w", err)
	}

	plan, err := a.planner.Plan(ctx, agent.PlanRequest{
		ThreadID:    threadID,
		UserMessage: userMessage,
		History:     history,
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
		if riskClass == "" {
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

	if len(proposed) == 0 {
		proposed = defaultProposedActions(threadID, userMessage)
	}

	return agent.PlanResult{
		AssistantMessage: assistant,
		ProposedActions:  proposed,
	}, nil
}

func (a *App) loadPlannerHistory(threadID string, limit int) ([]agent.Message, error) {
	rows, err := a.db.Query(`
		SELECT role, content
		FROM messages
		WHERE thread_id = ?
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

func defaultProposedActions(threadID, userMessage string) []agent.ProposedAction {
	return []agent.ProposedAction{
		{
			Tool:          defaultActionTool,
			Args:          defaultActionArgs(threadID, userMessage),
			Justification: defaultActionJustification,
			RiskClass:     defaultActionRiskClass,
		},
	}
}

func defaultActionArgs(threadID, userMessage string) json.RawMessage {
	args, _ := json.Marshal(map[string]string{
		"thread_id": threadID,
		"summary":   userMessage,
	})
	return args
}

func riskClassForTool(tool string) string {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "demo_external_notify", "gmail_send_draft", "gmail_send_message":
		return "EXFILTRATION"
	case "artifact_put", "notes_write", "gmail_create_draft_reply":
		return "WRITE"
	default:
		return "HIGH"
	}
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
				continue
			}
			a.logger.Error("action executor failed to execute action", "action_id", actionID, "error", err)
		}
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
		SELECT action_id, expires_at
		FROM proposed_actions
		WHERE status = 'PENDING'
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	expiredActionIDs := make([]string, 0)
	for rows.Next() {
		var actionID string
		var expiresAtRaw string
		if err := rows.Scan(&actionID, &expiresAtRaw); err != nil {
			return err
		}
		expiresAt, err := parseTimestamp(expiresAtRaw)
		if err != nil {
			return fmt.Errorf("parse expires_at for action %s: %w", actionID, err)
		}
		if !expiresAt.After(now) {
			expiredActionIDs = append(expiredActionIDs, actionID)
		}
	}

	if len(expiredActionIDs) == 0 {
		return tx.Commit()
	}

	nowStr := now.Format(time.RFC3339Nano)
	for _, actionID := range expiredActionIDs {
		res, err := tx.Exec(`
			UPDATE proposed_actions
			SET status = 'REJECTED', rejection_reason = 'expired'
			WHERE action_id = ? AND status = 'PENDING'
		`, actionID)
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
		if err := insertAuditTx(tx, "action_expired", actionID, `{"reason":"expired"}`, nowStr); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	a.logger.Info("pending actions expired", "count", len(expiredActionIDs))
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
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
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
