package server

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	defaultDevToken                   = "dev-token"
	defaultOwnerID                    = "owner-dev"
	defaultActionExecutorPollInterval = 250 * time.Millisecond
)

var errIdempotencyConflict = errors.New("idempotency conflict")

type AppConfig struct {
	DBPath                  string
	DevToken                string
	ActionExecutorInterval  time.Duration
	DisableBackgroundWorker bool
}

type App struct {
	db                     *sql.DB
	token                  string
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

func New(cfg AppConfig) (*App, error) {
	if cfg.DBPath == "" {
		return nil, errors.New("db path is required")
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

	token := cfg.DevToken
	if token == "" {
		token = defaultDevToken
	}

	interval := cfg.ActionExecutorInterval
	if interval <= 0 {
		interval = defaultActionExecutorPollInterval
	}

	app := &App{
		db:                     db,
		token:                  token,
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
	mux.HandleFunc("/v1/chat/threads", a.handleThreads)
	mux.HandleFunc("/v1/chat/threads/", a.handleThreadSubroutes)
	mux.HandleFunc("/v1/approvals", a.handleApprovals)
	mux.HandleFunc("/v1/approvals/", a.handleApprovalSubroutes)
	mux.HandleFunc("/v1/audit", a.handleAudit)

	return a.authMiddleware(mux)
}

func (a *App) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authz := r.Header.Get("Authorization")
		if authz != "Bearer "+a.token {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
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

	assistant := "I prepared a proposed external action. Review it in Approvals before execution."
	actionID := newID("act")
	idempotencyKey := newID("idem")
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)
	expiresAt := now.Add(24 * time.Hour).Format(time.RFC3339Nano)

	args, _ := json.Marshal(map[string]string{
		"thread_id": threadID,
		"summary":   req.Content,
	})

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
	`, newID("msg"), threadID, assistant, nowStr); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to insert assistant message"})
		return
	}
	if _, err := tx.Exec(`
		INSERT INTO proposed_actions(
			action_id, user_id, source, source_id, tool, args_json, risk_class,
			justification, idempotency_key, status, rejection_reason, expires_at, created_at
		) VALUES(?, ?, 'chat', ?, 'demo_external_notify', ?, 'EXFILTRATION',
			'User requested external follow-up', ?, 'PENDING', '', ?, ?)
	`, actionID, a.ownerID, threadID, string(args), idempotencyKey, expiresAt, nowStr); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to insert action"})
		return
	}
	if err := insertAuditTx(tx, "action_proposed", actionID, string(args), nowStr); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to insert audit"})
		return
	}

	if err := tx.Commit(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to commit transaction"})
		return
	}

	writeJSON(w, http.StatusCreated, createMessageResponse{
		AssistantMessage: assistant,
		ActionID:         actionID,
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

	return tx.Commit()
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

	return tx.Commit()
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

	return tx.Commit()
}

func (a *App) threadExists(threadID string) bool {
	var one int
	err := a.db.QueryRow(`SELECT 1 FROM threads WHERE thread_id = ?`, threadID).Scan(&one)
	return err == nil
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
		log.Printf("action executor: expire pending actions: %v", err)
	}

	actionIDs, err := a.listApprovedActionIDs()
	if err != nil {
		log.Printf("action executor: list approved actions: %v", err)
		return
	}

	for _, actionID := range actionIDs {
		if err := a.executeApprovedAction(actionID); err != nil {
			if errors.Is(err, errIdempotencyConflict) {
				log.Printf("action executor: idempotency conflict for action %s", actionID)
				continue
			}
			log.Printf("action executor: execute action %s: %v", actionID, err)
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

	return tx.Commit()
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
