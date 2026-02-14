package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestEndToEndApprovalFlow(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "pincer-test.db")
	app, err := New(AppConfig{
		DBPath:   dbPath,
		DevToken: "test-token",
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() {
		_ = app.Close()
	})

	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	threadID := createThread(t, srv.URL, "test-token")
	postMessage(t, srv.URL, "test-token", threadID, "hello")

	pending := listApprovals(t, srv.URL, "test-token", "pending")
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending approval, got %d", len(pending))
	}

	approveAction(t, srv.URL, "test-token", pending[0].ActionID)

	executed := waitForApprovalStatus(t, srv.URL, "test-token", "executed", pending[0].ActionID, 5*time.Second)
	if len(executed) != 1 {
		t.Fatalf("expected 1 executed approval, got %d", len(executed))
	}

	events := listAudit(t, srv.URL, "test-token")
	if countAuditEvents(events, pending[0].ActionID, "action_proposed") != 1 {
		t.Fatalf("expected action_proposed audit for %s", pending[0].ActionID)
	}
	if countAuditEvents(events, pending[0].ActionID, "action_approved") != 1 {
		t.Fatalf("expected action_approved audit for %s", pending[0].ActionID)
	}
	if countAuditEvents(events, pending[0].ActionID, "action_executed") != 1 {
		t.Fatalf("expected action_executed audit for %s", pending[0].ActionID)
	}
}

func TestRejectPendingAction(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "pincer-test.db")
	app, err := New(AppConfig{
		DBPath:   dbPath,
		DevToken: "test-token",
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() {
		_ = app.Close()
	})

	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	threadID := createThread(t, srv.URL, "test-token")
	postMessage(t, srv.URL, "test-token", threadID, "reject me")

	pending := listApprovals(t, srv.URL, "test-token", "pending")
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending approval, got %d", len(pending))
	}

	rejectAction(t, srv.URL, "test-token", pending[0].ActionID, "declined")

	rejected := waitForApprovalStatus(t, srv.URL, "test-token", "rejected", pending[0].ActionID, 2*time.Second)
	if len(rejected) != 1 {
		t.Fatalf("expected 1 rejected approval, got %d", len(rejected))
	}
	if rejected[0].RejectionReason != "declined" {
		t.Fatalf("expected rejection reason 'declined', got %q", rejected[0].RejectionReason)
	}

	events := listAudit(t, srv.URL, "test-token")
	if countAuditEvents(events, pending[0].ActionID, "action_rejected") != 1 {
		t.Fatalf("expected action_rejected audit for %s", pending[0].ActionID)
	}
}

func TestApproveRejectNonPendingReturnsConflict(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "pincer-test.db")
	app, err := New(AppConfig{
		DBPath:   dbPath,
		DevToken: "test-token",
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() {
		_ = app.Close()
	})

	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	threadID := createThread(t, srv.URL, "test-token")
	postMessage(t, srv.URL, "test-token", threadID, "approve then retry")

	pending := listApprovals(t, srv.URL, "test-token", "pending")
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending approval, got %d", len(pending))
	}
	actionID := pending[0].ActionID

	approveAction(t, srv.URL, "test-token", actionID)
	_ = waitForApprovalStatus(t, srv.URL, "test-token", "executed", actionID, 5*time.Second)

	approveStatus := postApprovalAction(t, srv.URL, "test-token", actionID, "approve", []byte(`{}`))
	if approveStatus != http.StatusConflict {
		t.Fatalf("expected approve retry status 409, got %d", approveStatus)
	}

	rejectStatus := postApprovalAction(t, srv.URL, "test-token", actionID, "reject", []byte(`{"reason":"too_late"}`))
	if rejectStatus != http.StatusConflict {
		t.Fatalf("expected reject on non-pending status 409, got %d", rejectStatus)
	}
}

func TestPendingActionExpiresToRejected(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "pincer-test.db")
	app, err := New(AppConfig{
		DBPath:   dbPath,
		DevToken: "test-token",
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() {
		_ = app.Close()
	})

	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	threadID := createThread(t, srv.URL, "test-token")
	postMessage(t, srv.URL, "test-token", threadID, "expire me")

	pending := listApprovals(t, srv.URL, "test-token", "pending")
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending approval, got %d", len(pending))
	}
	actionID := pending[0].ActionID

	expiresAt := time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339Nano)
	if _, err := app.db.Exec(`
		UPDATE proposed_actions
		SET expires_at = ?
		WHERE action_id = ?
	`, expiresAt, actionID); err != nil {
		t.Fatalf("expire pending action: %v", err)
	}

	rejected := waitForApprovalStatus(t, srv.URL, "test-token", "rejected", actionID, 5*time.Second)
	if len(rejected) != 1 {
		t.Fatalf("expected 1 rejected approval, got %d", len(rejected))
	}
	if rejected[0].RejectionReason != "expired" {
		t.Fatalf("expected rejection reason 'expired', got %q", rejected[0].RejectionReason)
	}

	events := waitForAuditEvent(t, srv.URL, "test-token", actionID, "action_expired", 5*time.Second)
	if len(events) != 1 {
		t.Fatalf("expected 1 action_expired event for %s, got %d", actionID, len(events))
	}
}

func TestExecuteApprovedActionIdempotencyConflict(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "pincer-test.db")
	app, err := New(AppConfig{
		DBPath:                  dbPath,
		DevToken:                "test-token",
		DisableBackgroundWorker: true,
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() {
		_ = app.Close()
	})

	now := time.Now().UTC().Format(time.RFC3339Nano)
	expiresAt := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339Nano)

	if err := insertApprovedActionForTest(app, "act_primary", "idem_conflict", `{"summary":"one"}`, expiresAt, now); err != nil {
		t.Fatalf("insert primary action: %v", err)
	}
	if err := app.executeApprovedAction("act_primary"); err != nil {
		t.Fatalf("execute primary action: %v", err)
	}
	if _, err := app.db.Exec(`
		UPDATE proposed_actions
		SET idempotency_key = 'idem_conflict_archived'
		WHERE action_id = 'act_primary'
	`); err != nil {
		t.Fatalf("archive primary action idempotency key: %v", err)
	}

	if err := insertApprovedActionForTest(app, "act_conflict", "idem_conflict", `{"summary":"two"}`, expiresAt, now); err != nil {
		t.Fatalf("insert conflict action: %v", err)
	}

	err = app.executeApprovedAction("act_conflict")
	if !errors.Is(err, errIdempotencyConflict) {
		t.Fatalf("expected errIdempotencyConflict, got %v", err)
	}

	var status string
	var rejectionReason string
	if err := app.db.QueryRow(`
		SELECT status, rejection_reason
		FROM proposed_actions
		WHERE action_id = 'act_conflict'
	`).Scan(&status, &rejectionReason); err != nil {
		t.Fatalf("load conflict action: %v", err)
	}
	if status != "REJECTED" {
		t.Fatalf("expected conflict action status REJECTED, got %s", status)
	}
	if rejectionReason != "idempotency_conflict" {
		t.Fatalf("expected rejection_reason idempotency_conflict, got %q", rejectionReason)
	}

	var conflictAuditCount int
	if err := app.db.QueryRow(`
		SELECT COUNT(*)
		FROM audit_log
		WHERE event_type = 'idempotency_conflict' AND entity_id = 'act_conflict'
	`).Scan(&conflictAuditCount); err != nil {
		t.Fatalf("count idempotency conflict audit events: %v", err)
	}
	if conflictAuditCount != 1 {
		t.Fatalf("expected 1 idempotency_conflict event, got %d", conflictAuditCount)
	}
}

type testThreadResponse struct {
	ThreadID string `json:"thread_id"`
}

type testApproval struct {
	ActionID        string `json:"action_id"`
	Status          string `json:"status"`
	RejectionReason string `json:"rejection_reason"`
}

type testApprovalsResponse struct {
	Items []testApproval `json:"items"`
}

type testAuditEvent struct {
	EventType string `json:"event_type"`
	EntityID  string `json:"entity_id"`
}

type testAuditResponse struct {
	Items []testAuditEvent `json:"items"`
}

func createThread(t *testing.T, baseURL, token string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/threads", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create thread status: %d", resp.StatusCode)
	}
	var out testThreadResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode thread: %v", err)
	}
	return out.ThreadID
}

func postMessage(t *testing.T, baseURL, token, threadID, content string) {
	t.Helper()
	body := []byte(`{"content":"` + content + `"}`)
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/threads/"+threadID+"/messages", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("post message status: %d", resp.StatusCode)
	}
}

func listApprovals(t *testing.T, baseURL, token, status string) []testApproval {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/approvals?status="+status, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list approvals status: %d", resp.StatusCode)
	}
	var out testApprovalsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode approvals: %v", err)
	}
	return out.Items
}

func approveAction(t *testing.T, baseURL, token, actionID string) {
	t.Helper()
	statusCode := postApprovalAction(t, baseURL, token, actionID, "approve", []byte(`{}`))
	if statusCode != http.StatusOK {
		t.Fatalf("approve status: %d", statusCode)
	}
}

func rejectAction(t *testing.T, baseURL, token, actionID, reason string) {
	t.Helper()
	body := []byte(fmt.Sprintf(`{"reason":%q}`, reason))
	statusCode := postApprovalAction(t, baseURL, token, actionID, "reject", body)
	if statusCode != http.StatusOK {
		t.Fatalf("reject status: %d", statusCode)
	}
}

func postApprovalAction(t *testing.T, baseURL, token, actionID, action string, body []byte) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/approvals/"+actionID+"/"+action, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func listAudit(t *testing.T, baseURL, token string) []testAuditEvent {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/audit", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list audit status: %d", resp.StatusCode)
	}
	var out testAuditResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	return out.Items
}

func waitForApprovalStatus(t *testing.T, baseURL, token, status, actionID string, timeout time.Duration) []testApproval {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		items := listApprovals(t, baseURL, token, status)
		for _, item := range items {
			if item.ActionID == actionID {
				return []testApproval{item}
			}
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func waitForAuditEvent(t *testing.T, baseURL, token, actionID, eventType string, timeout time.Duration) []testAuditEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		events := listAudit(t, baseURL, token)
		matches := make([]testAuditEvent, 0)
		for _, event := range events {
			if event.EntityID == actionID && event.EventType == eventType {
				matches = append(matches, event)
			}
		}
		if len(matches) > 0 {
			return matches
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func countAuditEvents(events []testAuditEvent, actionID, eventType string) int {
	count := 0
	for _, event := range events {
		if event.EntityID == actionID && event.EventType == eventType {
			count++
		}
	}
	return count
}

func insertApprovedActionForTest(app *App, actionID, idempotencyKey, argsJSON, expiresAt, createdAt string) error {
	_, err := app.db.Exec(`
		INSERT INTO proposed_actions(
			action_id, user_id, source, source_id, tool, args_json, risk_class,
			justification, idempotency_key, status, rejection_reason, expires_at, created_at
		) VALUES(?, ?, 'job', ?, 'demo_external_notify', ?, 'EXFILTRATION',
			'test action', ?, 'APPROVED', '', ?, ?)
	`, actionID, defaultOwnerID, "job-test", argsJSON, idempotencyKey, expiresAt, createdAt)
	if err != nil {
		return err
	}
	return nil
}
