package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	protocolv1 "github.com/lox/pincer/gen/proto/pincer/protocol/v1"
	"github.com/lox/pincer/gen/proto/pincer/protocol/v1/protocolv1connect"
	"github.com/lox/pincer/internal/agent"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestEndToEndApprovalFlow(t *testing.T) {
	t.Parallel()

	app := newTestAppWithPlanner(t, stubPlanner{
		result: agent.PlanResult{
			AssistantMessage: "Harness response ready.",
			ProposedActions: []agent.ProposedAction{
				{
					Tool:          "job_tool",
					Args:          json.RawMessage(`{"thread_id":"t","summary":"planner args"}`),
					Justification: "Planner requested external follow-up.",
					RiskClass:     "EXFILTRATION",
				},
			},
		},
	})
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

func TestPostMessageNoImplicitProposalByDefault(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapAuthToken(t, srv.URL)
	threadID := createThread(t, srv.URL, token)
	response := postMessageResponse(t, srv.URL, token, threadID, "hello")

	if response.AssistantMessage == "" {
		t.Fatalf("expected assistant message")
	}

	pending := listApprovals(t, srv.URL, token, "pending")
	if len(pending) != 0 {
		t.Fatalf("expected 0 pending approvals, got %d", len(pending))
	}
}

func TestPostMessageUsesPlannerOutput(t *testing.T) {
	t.Parallel()

	planner := stubPlanner{
		result: agent.PlanResult{
			AssistantMessage: "Harness response ready.",
			ProposedActions: []agent.ProposedAction{
				{
					Tool:          "job_tool",
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

	if response.AssistantMessage != "" {
		t.Fatalf("expected no assistant message when proposal is emitted, got %q", response.AssistantMessage)
	}

	pending := listApprovals(t, srv.URL, token, "pending")
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending approval, got %d", len(pending))
	}
	if pending[0].Tool != "job_tool" {
		t.Fatalf("expected tool job_tool, got %q", pending[0].Tool)
	}
}

func TestPostMessageWithProposalSkipsAssistantChatBubble(t *testing.T) {
	t.Parallel()

	app := newTestAppWithPlanner(t, stubPlanner{
		result: agent.PlanResult{
			AssistantMessage: "Proposing command execution.",
			ProposedActions: []agent.ProposedAction{
				{
					Tool:          "run_bash",
					Args:          json.RawMessage(`{"command":"deploy"}`),
					Justification: "User requested shell command.",
					RiskClass:     "HIGH",
					},
					},
					},
					})
					srv := httptest.NewServer(app.Handler())
					defer srv.Close()

					token := bootstrapAuthToken(t, srv.URL)
					threadID := createThread(t, srv.URL, token)
					postMessage(t, srv.URL, token, threadID, "run deploy")

					msgs := listMessages(t, srv.URL, token, threadID)
					if len(msgs) != 1 {
					t.Fatalf("expected only 1 message in thread, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Fatalf("expected first message role user, got %q", msgs[0].Role)
	}
}

func TestApproveChatActionWritesApprovedSystemMarker(t *testing.T) {
	t.Parallel()

	app := newTestAppWithPlanner(t, stubPlanner{
		result: agent.PlanResult{
			AssistantMessage: "Proposing command execution.",
			ProposedActions: []agent.ProposedAction{
				{
					Tool:          "run_bash",
					Args:          json.RawMessage(`{"command":"deploy"}`),
					Justification: "User requested shell command.",
					RiskClass:     "HIGH",
					},
					},
					},
					})
					srv := httptest.NewServer(app.Handler())
					defer srv.Close()

					token := bootstrapAuthToken(t, srv.URL)
					threadID := createThread(t, srv.URL, token)
					postMessage(t, srv.URL, token, threadID, "run deploy")

					pending := listApprovals(t, srv.URL, token, "pending")
					if len(pending) != 1 {
					t.Fatalf("expected 1 pending approval, got %d", len(pending))
					}
					actionID := pending[0].ActionID
					approveAction(t, srv.URL, token, actionID)

	messages := listMessages(t, srv.URL, token, threadID)
	approvedMarker := fmt.Sprintf("Action %s approved.", actionID)

	for _, msg := range messages {
		if msg.Role == "system" && strings.Contains(msg.Content, approvedMarker) {
			return
		}
	}

	t.Fatalf("expected system message to include %q", approvedMarker)
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

	app := newTestAppWithPlanner(t, stubPlanner{
		result: agent.PlanResult{
			AssistantMessage: "Proposing command execution.",
			ProposedActions: []agent.ProposedAction{
				{
					Tool:          "run_bash",
					Args:          json.RawMessage(`{"command":"deploy"}`),
					Justification: "User requested shell command.",
					RiskClass:     "HIGH",
					},
					},
					},
					})
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

	app := newTestAppWithPlanner(t, stubPlanner{
		result: agent.PlanResult{
			AssistantMessage: "Proposing command execution.",
			ProposedActions: []agent.ProposedAction{
				{
					Tool:          "run_bash",
					Args:          json.RawMessage(`{"command":"deploy"}`),
					Justification: "User requested shell command.",
					RiskClass:     "HIGH",
					},
					},
					},
					})
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

	app := newTestAppWithPlanner(t, stubPlanner{
		result: agent.PlanResult{
			AssistantMessage: "Proposing command execution.",
			ProposedActions: []agent.ProposedAction{
				{
					Tool:          "run_bash",
					Args:          json.RawMessage(`{"command":"deploy"}`),
					Justification: "User requested shell command.",
					RiskClass:     "HIGH",
					},
					},
					},
					})
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

func TestExecuteApprovedRunBashActionWritesCommandOutputToChat(t *testing.T) {
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
	threadID := "thr_bash"

	if _, err := app.db.Exec(`
		INSERT INTO threads(thread_id, user_id, channel, created_at)
		VALUES(?, ?, 'ios', ?)
	`, threadID, defaultOwnerID, now); err != nil {
		t.Fatalf("insert thread: %v", err)
	}

	if err := insertApprovedActionWithFieldsForTest(
		app,
		"act_bash",
		"idem_bash",
		"chat",
		threadID,
		"run_bash",
		`{"command":"printf hello"}`,
		"HIGH",
		expiresAt,
		now,
	); err != nil {
		t.Fatalf("insert bash action: %v", err)
	}

	if err := app.executeApprovedAction("act_bash"); err != nil {
		t.Fatalf("execute bash action: %v", err)
	}

	var status string
	if err := app.db.QueryRow(`SELECT status FROM proposed_actions WHERE action_id = 'act_bash'`).Scan(&status); err != nil {
		t.Fatalf("load action status: %v", err)
	}
	if status != "EXECUTED" {
		t.Fatalf("expected action status EXECUTED, got %q", status)
	}

	var systemContent string
	if err := app.db.QueryRow(`
		SELECT content
		FROM messages
		WHERE thread_id = ? AND role = 'system'
		ORDER BY created_at DESC
		LIMIT 1
	`, threadID).Scan(&systemContent); err != nil {
		t.Fatalf("load system message: %v", err)
	}

	if !strings.Contains(systemContent, "Command: printf hello") {
		t.Fatalf("expected system message to include command, got %q", systemContent)
	}
	if !strings.Contains(systemContent, "Exit code: 0") {
		t.Fatalf("expected system message to include exit code, got %q", systemContent)
	}
	if !strings.Contains(systemContent, "hello") {
		t.Fatalf("expected system message to include command output, got %q", systemContent)
	}
}

func TestExecuteBashActionStreamingHonorsTimeoutMs(t *testing.T) {
	t.Parallel()

	short := executeBashAction(`{"command":"sleep 0.15; echo done","timeout_ms":50}`)
	if !short.TimedOut {
		t.Fatalf("expected short timeout to time out")
	}
	if short.ExitCode != -1 {
		t.Fatalf("expected timed-out command to use exit code -1, got %d", short.ExitCode)
	}
	if !strings.Contains(short.Output, "command timed out after 50ms") {
		t.Fatalf("expected timeout output line, got %q", short.Output)
	}

	longer := executeBashAction(`{"command":"sleep 0.15; echo done","timeout_ms":500}`)
	if longer.TimedOut {
		t.Fatalf("expected longer timeout to complete without timing out")
	}
	if longer.ExitCode != 0 {
		t.Fatalf("expected successful exit code 0, got %d", longer.ExitCode)
	}
	if !strings.Contains(longer.Output, "done") {
		t.Fatalf("expected output to include done, got %q", longer.Output)
	}
}

func TestBoundedBashExecTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		timeoutMS int64
		want      time.Duration
	}{
		{
			name:      "uses default when unset",
			timeoutMS: 0,
			want:      defaultBashExecTimeout,
		},
		{
			name:      "keeps requested timeout when within bounds",
			timeoutMS: 2_500,
			want:      2500 * time.Millisecond,
		},
		{
			name:      "caps timeout to max bound",
			timeoutMS: int64((maxBashExecTimeout / time.Millisecond) + 1_000),
			want:      maxBashExecTimeout,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := boundedBashExecTimeout(tc.timeoutMS)
			if got != tc.want {
				t.Fatalf("boundedBashExecTimeout(%d) = %s, want %s", tc.timeoutMS, got, tc.want)
			}
		})
	}
}

func TestPlanTurnClassifiesRunBashRiskFromCommand(t *testing.T) {
	t.Parallel()

	app := newTestAppWithPlanner(t, stubPlanner{
		result: agent.PlanResult{
			AssistantMessage: "Ready.",
			ProposedActions: []agent.ProposedAction{
				{
					Tool:          "run_bash",
					Args:          json.RawMessage(`{"command":"rm -rf /tmp/pincer-risk-test"}`),
					Justification: "Requested command execution.",
					RiskClass:     "LOW",
				},
			},
		},
	})

	plan, err := app.planTurn(context.Background(), "thr_risk", "run command")
	if err != nil {
		t.Fatalf("plan turn: %v", err)
	}
	if len(plan.ProposedActions) != 1 {
		t.Fatalf("expected 1 proposed action, got %d", len(plan.ProposedActions))
	}
	if plan.ProposedActions[0].RiskClass != "HIGH" {
		t.Fatalf("expected trusted bash risk classification HIGH, got %q", plan.ProposedActions[0].RiskClass)
	}
}

func TestPlanTurnNormalizesRunBashTimeoutToMaxBound(t *testing.T) {
	t.Parallel()

	app := newTestAppWithPlanner(t, stubPlanner{
		result: agent.PlanResult{
			AssistantMessage: "Ready.",
			ProposedActions: []agent.ProposedAction{
				{
					Tool:          "run_bash",
					Args:          json.RawMessage(`{"command":"sleep 1","timeout_ms":999999999}`),
					Justification: "Requested command execution.",
					RiskClass:     "LOW",
				},
			},
		},
	})

	plan, err := app.planTurn(context.Background(), "thr_timeout", "run command")
	if err != nil {
		t.Fatalf("plan turn: %v", err)
	}
	if len(plan.ProposedActions) != 1 {
		t.Fatalf("expected 1 proposed action, got %d", len(plan.ProposedActions))
	}

	var args bashActionArgs
	if err := json.Unmarshal(plan.ProposedActions[0].Args, &args); err != nil {
		t.Fatalf("unmarshal normalized args: %v", err)
	}
	wantTimeoutMS := int64(maxBashExecTimeout / time.Millisecond)
	if args.TimeoutMS != wantTimeoutMS {
		t.Fatalf("expected timeout_ms=%d, got %d", wantTimeoutMS, args.TimeoutMS)
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
	app, err := New(AppConfig{
		DBPath:  filepath.Join(t.TempDir(), "pincer-test.db"),
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
	client := protocolv1connect.NewAuthServiceClient(connectHTTPClient(token), baseURL)
	resp, err := client.CreatePairingCode(context.Background(), connect.NewRequest(&protocolv1.CreatePairingCodeRequest{}))
	if err != nil {
		t.Fatalf("create pairing code: %v", err)
	}
	if resp.Msg.GetCode() == "" {
		t.Fatalf("pairing code is empty")
	}
	return resp.Msg.GetCode()
}

func createPairingCodeStatus(t *testing.T, baseURL, token string) int {
	t.Helper()
	client := protocolv1connect.NewAuthServiceClient(connectHTTPClient(token), baseURL)
	_, err := client.CreatePairingCode(context.Background(), connect.NewRequest(&protocolv1.CreatePairingCodeRequest{}))
	if err != nil {
		return connectErrorToHTTPStatus(err)
	}
	return http.StatusCreated
}

func bindPairing(t *testing.T, baseURL, code, deviceName string) string {
	t.Helper()
	client := protocolv1connect.NewAuthServiceClient(http.DefaultClient, baseURL)
	resp, err := client.BindPairingCode(context.Background(), connect.NewRequest(&protocolv1.BindPairingCodeRequest{
		Code:       code,
		DeviceName: deviceName,
	}))
	if err != nil {
		t.Fatalf("bind pairing code: %v", err)
	}
	if resp.Msg.GetToken() == "" {
		t.Fatalf("bind pairing token is empty")
	}
	return resp.Msg.GetToken()
}

func bindPairingStatus(t *testing.T, baseURL, code, deviceName string) int {
	t.Helper()
	client := protocolv1connect.NewAuthServiceClient(http.DefaultClient, baseURL)
	_, err := client.BindPairingCode(context.Background(), connect.NewRequest(&protocolv1.BindPairingCodeRequest{
		Code:       code,
		DeviceName: deviceName,
	}))
	if err != nil {
		return connectErrorToHTTPStatus(err)
	}
	return http.StatusCreated
}

func createThread(t *testing.T, baseURL, token string) string {
	t.Helper()
	client := protocolv1connect.NewThreadsServiceClient(connectHTTPClient(token), baseURL)
	resp, err := client.CreateThread(context.Background(), connect.NewRequest(&protocolv1.CreateThreadRequest{}))
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if resp.Msg.GetThreadId() == "" {
		t.Fatalf("thread id is empty")
	}
	return resp.Msg.GetThreadId()
}

func createThreadStatus(t *testing.T, baseURL, token string) int {
	t.Helper()
	client := protocolv1connect.NewThreadsServiceClient(connectHTTPClient(token), baseURL)
	_, err := client.CreateThread(context.Background(), connect.NewRequest(&protocolv1.CreateThreadRequest{}))
	if err != nil {
		return connectErrorToHTTPStatus(err)
	}
	return http.StatusCreated
}

func postMessage(t *testing.T, baseURL, token, threadID, content string) {
	t.Helper()
	_ = postMessageResponse(t, baseURL, token, threadID, content)
}

func postMessageResponse(t *testing.T, baseURL, token, threadID, content string) createMessageResponse {
	t.Helper()
	client := protocolv1connect.NewTurnsServiceClient(connectHTTPClient(token), baseURL)
	stream, err := client.StartTurn(context.Background(), connect.NewRequest(&protocolv1.StartTurnRequest{
		ThreadId:    threadID,
		UserText:    content,
		TriggerType: protocolv1.TriggerType_CHAT_MESSAGE,
	}))
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}

	var out createMessageResponse
	for stream.Receive() {
		event := stream.Msg()
		if msg := event.GetAssistantMessageCommitted(); msg != nil {
			out.AssistantMessage = msg.GetFullText()
		}
		if proposal := event.GetProposedActionCreated(); proposal != nil && out.ActionID == "" {
			out.ActionID = proposal.GetActionId()
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("turn stream error: %v", err)
	}
	return out
}

func listDevices(t *testing.T, baseURL, token string) []testDevice {
	t.Helper()
	client := protocolv1connect.NewDevicesServiceClient(connectHTTPClient(token), baseURL)
	resp, err := client.ListDevices(context.Background(), connect.NewRequest(&protocolv1.ListDevicesRequest{}))
	if err != nil {
		t.Fatalf("list devices: %v", err)
	}
	items := make([]testDevice, 0, len(resp.Msg.GetItems()))
	for _, item := range resp.Msg.GetItems() {
		items = append(items, testDevice{
			DeviceID:  item.GetDeviceId(),
			Name:      item.GetName(),
			RevokedAt: formatTimestamp(item.GetRevokedAt()),
			IsCurrent: item.GetIsCurrent(),
		})
	}
	return items
}

func listMessages(t *testing.T, baseURL, token, threadID string) []message {
	t.Helper()
	client := protocolv1connect.NewThreadsServiceClient(connectHTTPClient(token), baseURL)
	resp, err := client.ListThreadMessages(context.Background(), connect.NewRequest(&protocolv1.ListThreadMessagesRequest{
		ThreadId: threadID,
	}))
	if err != nil {
		t.Fatalf("list thread messages: %v", err)
	}
	items := make([]message, 0, len(resp.Msg.GetItems()))
	for _, item := range resp.Msg.GetItems() {
		items = append(items, message{
			MessageID: item.GetMessageId(),
			ThreadID:  threadID,
			Role:      item.GetRole(),
			Content:   item.GetContent(),
			CreatedAt: formatTimestamp(item.GetCreatedAt()),
		})
	}
	return items
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
	client := protocolv1connect.NewDevicesServiceClient(connectHTTPClient(token), baseURL)
	_, err := client.RevokeDevice(context.Background(), connect.NewRequest(&protocolv1.RevokeDeviceRequest{
		DeviceId: deviceID,
	}))
	if err != nil {
		return connectErrorToHTTPStatus(err)
	}
	return http.StatusOK
}

func listApprovals(t *testing.T, baseURL, token, status string) []testApproval {
	t.Helper()
	client := protocolv1connect.NewApprovalsServiceClient(connectHTTPClient(token), baseURL)
	resp, err := client.ListApprovals(context.Background(), connect.NewRequest(&protocolv1.ListApprovalsRequest{
		Status: actionStatusFromString(status),
	}))
	if err != nil {
		t.Fatalf("list approvals: %v", err)
	}
	items := make([]testApproval, 0, len(resp.Msg.GetItems()))
	for _, item := range resp.Msg.GetItems() {
		items = append(items, testApproval{
			ActionID:        item.GetActionId(),
			Tool:            item.GetTool(),
			Status:          strings.ToLower(item.GetStatus().String()),
			RejectionReason: item.GetRejectionReason(),
		})
	}
	return items
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
	client := protocolv1connect.NewApprovalsServiceClient(connectHTTPClient(token), baseURL)

	switch action {
	case "approve":
		_, err := client.ApproveAction(context.Background(), connect.NewRequest(&protocolv1.ApproveActionRequest{
			ActionId: actionID,
		}))
		if err != nil {
			return connectErrorToHTTPStatus(err)
		}
		return http.StatusOK
	case "reject":
		var reqBody rejectActionRequest
		if err := json.Unmarshal(body, &reqBody); err != nil {
			return http.StatusBadRequest
		}
		_, err := client.RejectAction(context.Background(), connect.NewRequest(&protocolv1.RejectActionRequest{
			ActionId: actionID,
			Reason:   reqBody.Reason,
		}))
		if err != nil {
			return connectErrorToHTTPStatus(err)
		}
		return http.StatusOK
	default:
		t.Fatalf("unsupported approval action %q", action)
		return http.StatusInternalServerError
	}
}

func listAudit(t *testing.T, baseURL, token string) []testAuditEvent {
	t.Helper()
	client := protocolv1connect.NewSystemServiceClient(connectHTTPClient(token), baseURL)
	resp, err := client.ListAudit(context.Background(), connect.NewRequest(&protocolv1.ListAuditRequest{}))
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	items := make([]testAuditEvent, 0, len(resp.Msg.GetItems()))
	for _, item := range resp.Msg.GetItems() {
		items = append(items, testAuditEvent{
			EventType: item.GetEventType(),
			EntityID:  item.GetActionId(),
		})
	}
	return items
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
	return insertApprovedActionWithFieldsForTest(
		app,
		actionID,
		idempotencyKey,
		"job",
		"job-test",
		"job_tool",
		argsJSON,
		"EXFILTRATION",
		expiresAt,
		createdAt,
	)
}

func insertApprovedActionWithFieldsForTest(
	app *App,
	actionID string,
	idempotencyKey string,
	source string,
	sourceID string,
	tool string,
	argsJSON string,
	riskClass string,
	expiresAt string,
	createdAt string,
) error {
	_, err := app.db.Exec(`
		INSERT INTO proposed_actions(
			action_id, user_id, source, source_id, tool, args_json, risk_class,
			justification, idempotency_key, status, rejection_reason, expires_at, created_at
		) VALUES(?, ?, ?, ?, ?, ?, ?,
			'test action', ?, 'APPROVED', '', ?, ?)
	`, actionID, defaultOwnerID, source, sourceID, tool, argsJSON, riskClass, idempotencyKey, expiresAt, createdAt)
	if err != nil {
		return err
	}
	return nil
}

func connectHTTPClient(token string) *http.Client {
	if token == "" {
		return http.DefaultClient
	}
	return newAuthorizedHTTPClient(token)
}

func actionStatusFromString(status string) protocolv1.ActionStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pending":
		return protocolv1.ActionStatus_PENDING
	case "approved":
		return protocolv1.ActionStatus_APPROVED
	case "rejected":
		return protocolv1.ActionStatus_REJECTED
	case "executed":
		return protocolv1.ActionStatus_EXECUTED
	default:
		return protocolv1.ActionStatus_ACTION_STATUS_UNSPECIFIED
	}
}

func formatTimestamp(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return ""
	}
	return ts.AsTime().UTC().Format(time.RFC3339Nano)
}

func connectErrorToHTTPStatus(err error) int {
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		return http.StatusInternalServerError
	}

	switch connectErr.Code() {
	case connect.CodeUnauthenticated:
		return http.StatusUnauthorized
	case connect.CodePermissionDenied:
		return http.StatusForbidden
	case connect.CodeNotFound:
		return http.StatusNotFound
	case connect.CodeAlreadyExists, connect.CodeAborted, connect.CodeFailedPrecondition:
		return http.StatusConflict
	case connect.CodeInvalidArgument:
		return http.StatusBadRequest
	case connect.CodeUnimplemented:
		return http.StatusNotImplemented
	default:
		return http.StatusInternalServerError
	}
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
