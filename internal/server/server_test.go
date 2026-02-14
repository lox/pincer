package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lox/pincer/internal/agent"
)

func TestEndToEndApprovalFlow(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapAuthToken(t, srv.URL)
	threadID := createThread(t, srv.URL, token)
	postMessage(t, srv.URL, token, threadID, "hello")

	pending := listApprovals(t, srv.URL, token, "pending")
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending approval, got %d", len(pending))
	}

	approveAction(t, srv.URL, token, pending[0].ActionID)

	executed := waitForApprovalStatus(t, srv.URL, token, "executed", pending[0].ActionID, 5*time.Second)
	if len(executed) != 1 {
		t.Fatalf("expected 1 executed approval, got %d", len(executed))
	}

	events := listAudit(t, srv.URL, token)
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

func TestPostMessageUsesPlannerOutput(t *testing.T) {
	t.Parallel()

	planner := stubPlanner{
		result: agent.PlanResult{
			AssistantMessage: "Harness response ready.",
			ProposedActions: []agent.ProposedAction{
				{
					Tool:          "demo_external_notify",
					Args:          json.RawMessage(`{"thread_id":"t","summary":"planner args"}`),
					Justification: "Planner requested external follow-up.",
					RiskClass:     "EXFILTRATION",
				},
			},
		},
	}

	app := newTestAppWithPlanner(t, planner)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapAuthToken(t, srv.URL)
	threadID := createThread(t, srv.URL, token)
	response := postMessageResponse(t, srv.URL, token, threadID, "hello harness")

	if response.AssistantMessage != "Harness response ready." {
		t.Fatalf("expected planner assistant message, got %q", response.AssistantMessage)
	}

	pending := listApprovals(t, srv.URL, token, "pending")
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending approval, got %d", len(pending))
	}
	if pending[0].Tool != "demo_external_notify" {
		t.Fatalf("expected tool demo_external_notify, got %q", pending[0].Tool)
	}
}

func TestProtectedEndpointRequiresToken(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	status := createThreadStatus(t, srv.URL, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("expected status 401 without token, got %d", status)
	}
}

func TestPairingCodeRequiresAuthAfterBootstrap(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	_ = bootstrapAuthToken(t, srv.URL)

	status := createPairingCodeStatus(t, srv.URL, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("expected status 401 for unauthenticated pairing code request, got %d", status)
	}
}

func TestPairingCodeOneTimeUse(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	code := createPairingCode(t, srv.URL, "")
	_ = bindPairing(t, srv.URL, code, "device-1")

	status := bindPairingStatus(t, srv.URL, code, "device-2")
	if status != http.StatusUnauthorized {
		t.Fatalf("expected status 401 for reused pairing code, got %d", status)
	}
}

func TestListDevicesAndRevokeInvalidatesToken(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapAuthToken(t, srv.URL)
	devices := listDevices(t, srv.URL, token)
	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}
	if devices[0].RevokedAt != "" {
		t.Fatalf("expected active device revoked_at to be empty, got %q", devices[0].RevokedAt)
	}

	deviceID := devices[0].DeviceID
	revokeDevice(t, srv.URL, token, deviceID)

	status := createThreadStatus(t, srv.URL, token)
	if status != http.StatusUnauthorized {
		t.Fatalf("expected revoked token to return 401, got %d", status)
	}

	pairingStatus := createPairingCodeStatus(t, srv.URL, "")
	if pairingStatus != http.StatusCreated {
		t.Fatalf("expected pairing bootstrap to reopen after revoking only device, got %d", pairingStatus)
	}
	token2 := bootstrapAuthToken(t, srv.URL)

	events := listAudit(t, srv.URL, token2)
	if countAuditEvents(events, deviceID, "device_revoked") != 1 {
		t.Fatalf("expected device_revoked audit for %s", deviceID)
	}
}

func TestRevokeUnknownDeviceReturnsNotFound(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapAuthToken(t, srv.URL)
	status := revokeDeviceStatus(t, srv.URL, token, "dev_missing")
	if status != http.StatusNotFound {
		t.Fatalf("expected status 404 for unknown device, got %d", status)
	}
}

func TestListDevicesMarksCurrentDevice(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token1 := bootstrapAuthToken(t, srv.URL)
	code2 := createPairingCode(t, srv.URL, token1)
	token2 := bindPairing(t, srv.URL, code2, "device-2")

	devices := listDevices(t, srv.URL, token2)
	if len(devices) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(devices))
	}

	currentCount := 0
	for _, device := range devices {
		if device.IsCurrent {
			currentCount++
			if device.Name != "device-2" {
				t.Fatalf("expected device-2 to be current, got %q", device.Name)
			}
			if device.RevokedAt != "" {
				t.Fatalf("expected current device to be active, revoked_at=%q", device.RevokedAt)
			}
		}
	}

	if currentCount != 1 {
		t.Fatalf("expected exactly 1 current device, got %d", currentCount)
	}
}

func TestRejectPendingAction(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapAuthToken(t, srv.URL)
	threadID := createThread(t, srv.URL, token)
	postMessage(t, srv.URL, token, threadID, "reject me")

	pending := listApprovals(t, srv.URL, token, "pending")
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending approval, got %d", len(pending))
	}

	rejectAction(t, srv.URL, token, pending[0].ActionID, "declined")

	rejected := waitForApprovalStatus(t, srv.URL, token, "rejected", pending[0].ActionID, 2*time.Second)
	if len(rejected) != 1 {
		t.Fatalf("expected 1 rejected approval, got %d", len(rejected))
	}
	if rejected[0].RejectionReason != "declined" {
		t.Fatalf("expected rejection reason 'declined', got %q", rejected[0].RejectionReason)
	}

	events := listAudit(t, srv.URL, token)
	if countAuditEvents(events, pending[0].ActionID, "action_rejected") != 1 {
		t.Fatalf("expected action_rejected audit for %s", pending[0].ActionID)
	}
}

func TestApproveRejectNonPendingReturnsConflict(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapAuthToken(t, srv.URL)
	threadID := createThread(t, srv.URL, token)
	postMessage(t, srv.URL, token, threadID, "approve then retry")

	pending := listApprovals(t, srv.URL, token, "pending")
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending approval, got %d", len(pending))
	}
	actionID := pending[0].ActionID

	approveAction(t, srv.URL, token, actionID)
	_ = waitForApprovalStatus(t, srv.URL, token, "executed", actionID, 5*time.Second)

	approveStatus := postApprovalAction(t, srv.URL, token, actionID, "approve", []byte(`{}`))
	if approveStatus != http.StatusConflict {
		t.Fatalf("expected approve retry status 409, got %d", approveStatus)
	}

	rejectStatus := postApprovalAction(t, srv.URL, token, actionID, "reject", []byte(`{"reason":"too_late"}`))
	if rejectStatus != http.StatusConflict {
		t.Fatalf("expected reject on non-pending status 409, got %d", rejectStatus)
	}
}

func TestPendingActionExpiresToRejected(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapAuthToken(t, srv.URL)
	threadID := createThread(t, srv.URL, token)
	postMessage(t, srv.URL, token, threadID, "expire me")

	pending := listApprovals(t, srv.URL, token, "pending")
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

	rejected := waitForApprovalStatus(t, srv.URL, token, "rejected", actionID, 5*time.Second)
	if len(rejected) != 1 {
		t.Fatalf("expected 1 rejected approval, got %d", len(rejected))
	}
	if rejected[0].RejectionReason != "expired" {
		t.Fatalf("expected rejection reason 'expired', got %q", rejected[0].RejectionReason)
	}

	events := waitForAuditEvent(t, srv.URL, token, actionID, "action_expired", 5*time.Second)
	if len(events) != 1 {
		t.Fatalf("expected 1 action_expired event for %s, got %d", actionID, len(events))
	}
}

func TestExecuteApprovedActionIdempotencyConflict(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "pincer-test.db")
	app, err := New(AppConfig{
		DBPath:                  dbPath,
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
	Tool            string `json:"tool"`
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

type testPairingCodeResponse struct {
	Code string `json:"code"`
}

type testPairingBindResponse struct {
	Token string `json:"token"`
}

type testDevice struct {
	DeviceID  string `json:"device_id"`
	Name      string `json:"name"`
	RevokedAt string `json:"revoked_at"`
	IsCurrent bool   `json:"is_current"`
}

type testDevicesResponse struct {
	Items []testDevice `json:"items"`
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "pincer-test.db")
	app, err := New(AppConfig{DBPath: dbPath})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() {
		_ = app.Close()
	})
	return app
}

func newTestAppWithPlanner(t *testing.T, planner agent.Planner) *App {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "pincer-test.db")
	app, err := New(AppConfig{
		DBPath:  dbPath,
		Planner: planner,
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() {
		_ = app.Close()
	})
	return app
}

func bootstrapAuthToken(t *testing.T, baseURL string) string {
	t.Helper()
	code := createPairingCode(t, baseURL, "")
	return bindPairing(t, baseURL, code, "test-device")
}

func createPairingCode(t *testing.T, baseURL, token string) string {
	t.Helper()
	status, body := postJSON(t, http.MethodPost, baseURL+"/v1/pairing/code", token, []byte(`{}`))
	if status != http.StatusCreated {
		t.Fatalf("create pairing code status: %d body=%s", status, string(body))
	}
	var out testPairingCodeResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode pairing code response: %v", err)
	}
	if out.Code == "" {
		t.Fatalf("pairing code is empty")
	}
	return out.Code
}

func createPairingCodeStatus(t *testing.T, baseURL, token string) int {
	t.Helper()
	status, _ := postJSON(t, http.MethodPost, baseURL+"/v1/pairing/code", token, []byte(`{}`))
	return status
}

func bindPairing(t *testing.T, baseURL, code, deviceName string) string {
	t.Helper()
	status, body := postJSON(t, http.MethodPost, baseURL+"/v1/pairing/bind", "", []byte(fmt.Sprintf(`{"code":%q,"device_name":%q}`, code, deviceName)))
	if status != http.StatusCreated {
		t.Fatalf("bind pairing status: %d body=%s", status, string(body))
	}
	var out testPairingBindResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode bind pairing response: %v", err)
	}
	if out.Token == "" {
		t.Fatalf("bind pairing token is empty")
	}
	return out.Token
}

func bindPairingStatus(t *testing.T, baseURL, code, deviceName string) int {
	t.Helper()
	status, _ := postJSON(t, http.MethodPost, baseURL+"/v1/pairing/bind", "", []byte(fmt.Sprintf(`{"code":%q,"device_name":%q}`, code, deviceName)))
	return status
}

func createThread(t *testing.T, baseURL, token string) string {
	t.Helper()
	status, body := postJSON(t, http.MethodPost, baseURL+"/v1/chat/threads", token, []byte(`{}`))
	if status != http.StatusCreated {
		t.Fatalf("create thread status: %d body=%s", status, string(body))
	}
	var out testThreadResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode thread: %v", err)
	}
	return out.ThreadID
}

func createThreadStatus(t *testing.T, baseURL, token string) int {
	t.Helper()
	status, _ := postJSON(t, http.MethodPost, baseURL+"/v1/chat/threads", token, []byte(`{}`))
	return status
}

func postMessage(t *testing.T, baseURL, token, threadID, content string) {
	t.Helper()
	status, body := postJSON(t, http.MethodPost, baseURL+"/v1/chat/threads/"+threadID+"/messages", token, []byte(`{"content":"`+content+`"}`))
	if status != http.StatusCreated {
		t.Fatalf("post message status: %d body=%s", status, string(body))
	}
}

func postMessageResponse(t *testing.T, baseURL, token, threadID, content string) createMessageResponse {
	t.Helper()
	status, body := postJSON(t, http.MethodPost, baseURL+"/v1/chat/threads/"+threadID+"/messages", token, []byte(`{"content":"`+content+`"}`))
	if status != http.StatusCreated {
		t.Fatalf("post message status: %d body=%s", status, string(body))
	}

	var out createMessageResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode createMessageResponse: %v", err)
	}
	return out
}

func listDevices(t *testing.T, baseURL, token string) []testDevice {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/devices", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list devices status: %d", resp.StatusCode)
	}
	var out testDevicesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode devices: %v", err)
	}
	return out.Items
}

func revokeDevice(t *testing.T, baseURL, token, deviceID string) {
	t.Helper()
	status := revokeDeviceStatus(t, baseURL, token, deviceID)
	if status != http.StatusOK {
		t.Fatalf("revoke device status: %d", status)
	}
}

func revokeDeviceStatus(t *testing.T, baseURL, token, deviceID string) int {
	t.Helper()
	status, _ := postJSON(t, http.MethodPost, baseURL+"/v1/devices/"+deviceID+"/revoke", token, []byte(`{}`))
	return status
}

func listApprovals(t *testing.T, baseURL, token, status string) []testApproval {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/approvals?status="+status, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
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
	status, _ := postJSON(t, http.MethodPost, baseURL+"/v1/approvals/"+actionID+"/"+action, token, body)
	return status
}

func listAudit(t *testing.T, baseURL, token string) []testAuditEvent {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/audit", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
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

func postJSON(t *testing.T, method, url, token string, body []byte) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return resp.StatusCode, respBody
}

type stubPlanner struct {
	result agent.PlanResult
	err    error
}

func (p stubPlanner) Plan(_ context.Context, _ agent.PlanRequest) (agent.PlanResult, error) {
	if p.err != nil {
		return agent.PlanResult{}, p.err
	}
	return p.result, nil
}
