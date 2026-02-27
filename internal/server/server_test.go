package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

	if response.AssistantMessage != "Harness response ready." {
		t.Fatalf("expected assistant message to be persisted with proposal, got %q", response.AssistantMessage)
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
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages in thread (user + assistant), got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Fatalf("expected first message role user, got %q", msgs[0].Role)
	}
	if msgs[1].Role != "assistant" {
		t.Fatalf("expected second message role assistant, got %q", msgs[1].Role)
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
		WorkspaceRoot:           filepath.Join(t.TempDir(), "workspace"),
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
		WorkspaceRoot:           filepath.Join(t.TempDir(), "workspace"),
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

func TestMaybeResumeTurnChatResetsContinuationBudget(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "pincer-test.db")
	app, err := New(AppConfig{
		DBPath:                  dbPath,
		WorkspaceRoot:           filepath.Join(t.TempDir(), "workspace"),
		DisableBackgroundWorker: true,
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() {
		_ = app.Close()
	})

	threadID := "thr_budget_chat"
	turnID := "turn_budget_chat"
	actionID := "act_budget_chat"
	now := time.Now().UTC()
	nowRaw := now.Format(time.RFC3339Nano)
	expiresAt := now.Add(24 * time.Hour).Format(time.RFC3339Nano)

	if _, err := app.db.Exec(`
		INSERT INTO threads(thread_id, user_id, channel, created_at)
		VALUES(?, ?, 'ios', ?)
	`, threadID, defaultOwnerID, nowRaw); err != nil {
		t.Fatalf("insert thread: %v", err)
	}

	if _, err := app.db.Exec(`
		INSERT INTO proposed_actions(
			action_id, user_id, source, source_id, tool, args_json, risk_class,
			justification, idempotency_key, status, rejection_reason, expires_at,
			created_at, turn_id
		) VALUES(?, ?, 'chat', ?, 'run_bash', ?, 'HIGH', ?, ?, 'EXECUTED', '', ?, ?, ?)
	`, actionID, defaultOwnerID, threadID, `{"command":"echo budget"}`, "budget test", "idem_budget_chat", expiresAt, nowRaw, turnID); err != nil {
		t.Fatalf("insert executed action: %v", err)
	}

	if _, err := app.appendThreadEvent(context.Background(), &protocolv1.ThreadEvent{
		ThreadId:     threadID,
		TurnId:       turnID,
		Source:       protocolv1.EventSource_SYSTEM,
		ContentTrust: protocolv1.ContentTrust_TRUSTED_SYSTEM,
		OccurredAt:   timestamppb.New(now),
		Payload: &protocolv1.ThreadEvent_TurnStarted{TurnStarted: &protocolv1.TurnStarted{
			TriggerType: protocolv1.TriggerType_CHAT_MESSAGE,
		}},
	}); err != nil {
		t.Fatalf("append turn started event: %v", err)
	}

	for i := 0; i < maxInlineToolSteps; i++ {
		createdAt := now.Add(time.Second + time.Duration(i)*time.Millisecond).Format(time.RFC3339Nano)
		if _, err := app.db.Exec(`
			INSERT INTO messages(message_id, thread_id, role, content, created_at)
			VALUES(?, ?, 'internal', ?, ?)
		`, newID("msg"), threadID, fmt.Sprintf("[tool_call:read_file] {\"path\":\"memory/%d.md\"}", i), createdAt); err != nil {
			t.Fatalf("insert internal tool call message %d: %v", i, err)
		}
	}

	app.maybeResumeTurn(actionID)

	waitForCondition(t, time.Second, func() bool {
		var continuationCount int
		if err := app.db.QueryRow(`
			SELECT COUNT(*)
			FROM work_items
			WHERE turn_id = ? AND kind = 'approval_resume'
		`, turnID).Scan(&continuationCount); err != nil {
			return false
		}
		return continuationCount > 0
	}, "expected approval_resume work item for chat continuation")

	var startStep int
	var maxSteps int
	var maxWallTimeMS int64
	if err := app.db.QueryRow(`
		SELECT start_step, max_tool_steps, max_wall_time_ms
		FROM work_items
		WHERE turn_id = ? AND kind = 'approval_resume'
		ORDER BY created_at DESC
		LIMIT 1
	`, turnID).Scan(&startStep, &maxSteps, &maxWallTimeMS); err != nil {
		t.Fatalf("load approval_resume work item: %v", err)
	}
	if startStep != 0 {
		t.Fatalf("expected chat continuation start_step to reset to 0, got %d", startStep)
	}
	if maxSteps != maxInlineToolSteps {
		t.Fatalf("expected chat continuation max_tool_steps=%d, got %d", maxInlineToolSteps, maxSteps)
	}
	wantWallMS := int64(defaultChatTurnMaxWallTime / time.Millisecond)
	if maxWallTimeMS != wantWallMS {
		t.Fatalf("expected chat continuation max_wall_time_ms=%d, got %d", wantWallMS, maxWallTimeMS)
	}
}

func TestMaybeResumeTurnJobStopsWhenMaxToolStepsExhausted(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "pincer-test.db")
	app, err := New(AppConfig{
		DBPath:                  dbPath,
		WorkspaceRoot:           filepath.Join(t.TempDir(), "workspace"),
		DisableBackgroundWorker: true,
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() {
		_ = app.Close()
	})

	originThreadID := "thr_budget_origin"
	jobThreadID := "thr_budget_job"
	turnID := "turn_budget_job"
	actionID := "act_budget_job"
	jobID := "job_budget_job"
	now := time.Now().UTC()
	nowRaw := now.Format(time.RFC3339Nano)
	expiresAt := now.Add(24 * time.Hour).Format(time.RFC3339Nano)

	for _, threadID := range []string{originThreadID, jobThreadID} {
		if _, err := app.db.Exec(`
			INSERT INTO threads(thread_id, user_id, channel, created_at)
			VALUES(?, ?, 'ios', ?)
		`, threadID, defaultOwnerID, nowRaw); err != nil {
			t.Fatalf("insert thread %s: %v", threadID, err)
		}
	}

	if _, err := app.db.Exec(`
		INSERT INTO jobs(
			job_id, user_id, goal, status, thread_id, origin_thread_id,
			trigger_type, trigger_source_id, max_tool_steps, max_wall_time_ms,
			current_turn_id, started_at, last_error, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, jobID, defaultOwnerID, "budget test", jobStatusWaitingApproval, jobThreadID, originThreadID,
		protocolv1.TriggerType_JOB_WAKEUP.String(), originThreadID, 3, int64(defaultJobMaxWallTime/time.Millisecond),
		turnID, nowRaw, "", nowRaw, nowRaw); err != nil {
		t.Fatalf("insert job: %v", err)
	}

	if _, err := app.db.Exec(`
		INSERT INTO proposed_actions(
			action_id, user_id, source, source_id, tool, args_json, risk_class,
			justification, idempotency_key, status, rejection_reason, expires_at,
			created_at, turn_id
		) VALUES(?, ?, 'job', ?, 'run_bash', ?, 'HIGH', ?, ?, 'EXECUTED', '', ?, ?, ?)
	`, actionID, defaultOwnerID, jobThreadID, `{"command":"echo budget"}`, "budget test", "idem_budget_job", expiresAt, nowRaw, turnID); err != nil {
		t.Fatalf("insert executed job action: %v", err)
	}

	if _, err := app.appendThreadEvent(context.Background(), &protocolv1.ThreadEvent{
		ThreadId:     jobThreadID,
		TurnId:       turnID,
		Source:       protocolv1.EventSource_SYSTEM,
		ContentTrust: protocolv1.ContentTrust_TRUSTED_SYSTEM,
		OccurredAt:   timestamppb.New(now),
		Payload: &protocolv1.ThreadEvent_TurnStarted{TurnStarted: &protocolv1.TurnStarted{
			TriggerType: protocolv1.TriggerType_JOB_WAKEUP,
		}},
	}); err != nil {
		t.Fatalf("append job turn started event: %v", err)
	}

	for i := 0; i < 3; i++ {
		createdAt := now.Add(time.Second + time.Duration(i)*time.Millisecond).Format(time.RFC3339Nano)
		if _, err := app.db.Exec(`
			INSERT INTO messages(message_id, thread_id, role, content, created_at)
			VALUES(?, ?, 'internal', ?, ?)
		`, newID("msg"), jobThreadID, fmt.Sprintf("[tool_call:read_file] {\"path\":\"memory/%d.md\"}", i), createdAt); err != nil {
			t.Fatalf("insert internal job tool call %d: %v", i, err)
		}
	}

	app.maybeResumeTurn(actionID)

	var continuationCount int
	if err := app.db.QueryRow(`
		SELECT COUNT(*)
		FROM work_items
		WHERE turn_id = ? AND kind = 'approval_resume'
	`, turnID).Scan(&continuationCount); err != nil {
		t.Fatalf("count approval_resume work items: %v", err)
	}
	if continuationCount != 0 {
		t.Fatalf("expected no approval_resume work item when job budget is exhausted, got %d", continuationCount)
	}

	var status string
	var lastError string
	if err := app.db.QueryRow(`
		SELECT status, last_error
		FROM jobs
		WHERE job_id = ?
	`, jobID).Scan(&status, &lastError); err != nil {
		t.Fatalf("load job status: %v", err)
	}
	if status != jobStatusPausedBudget {
		t.Fatalf("expected job status %q, got %q", jobStatusPausedBudget, status)
	}
	if lastError != "max_tool_steps_exhausted" {
		t.Fatalf("expected job last_error max_tool_steps_exhausted, got %q", lastError)
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

	plan, err := app.planTurn(context.Background(), "thr_risk", "run command", 0, 0, "")
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

	plan, err := app.planTurn(context.Background(), "thr_timeout", "run command", 0, 0, "")
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

func TestPlanTurnRewritesHeartbeatJobStatusBashToJobsList(t *testing.T) {
	t.Parallel()

	app := newTestAppWithPlanner(t, stubPlanner{
		result: agent.PlanResult{
			AssistantMessage: "Checking spawned job status.",
			ProposedActions: []agent.ProposedAction{
				{
					Tool:          "run_bash",
					Args:          json.RawMessage(`{"command":"ls /tmp/pincer/jobs/ 2>/dev/null && cat /tmp/pincer/jobs/*/status 2>/dev/null || echo \"No spawned jobs found\""}`),
					Justification: "Check spawned jobs.",
					RiskClass:     "HIGH",
				},
			},
		},
	})

	plan, err := app.planTurn(context.Background(), heartbeatThreadID, "heartbeat", 0, 0, "")
	if err != nil {
		t.Fatalf("plan turn: %v", err)
	}
	if len(plan.ProposedActions) != 1 {
		t.Fatalf("expected 1 proposed action, got %d", len(plan.ProposedActions))
	}
	if got := plan.ProposedActions[0].Tool; got != "jobs_list" {
		t.Fatalf("expected heartbeat status bash command to rewrite to jobs_list, got %q", got)
	}
	if got := plan.ProposedActions[0].RiskClass; got != "READ" {
		t.Fatalf("expected rewritten tool risk READ, got %q", got)
	}
	if got := strings.TrimSpace(string(plan.ProposedActions[0].Args)); got != "{}" {
		t.Fatalf("expected rewritten args {}, got %q", got)
	}
}

func TestWebFetchExecutesInlineAsReadTool(t *testing.T) {
	t.Parallel()

	// Start a local HTTP server to serve content for web_fetch.
	contentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok","data":"test-payload"}`)
	}))
	defer contentServer.Close()

	callCount := 0
	planner := &callCountPlanner{
		plans: []agent.PlanResult{
			{
				AssistantMessage: "Fetching the URL for you.",
				ProposedActions: []agent.ProposedAction{
					{
						Tool:          "web_fetch",
						Args:          json.RawMessage(fmt.Sprintf(`{"url":%q}`, contentServer.URL)),
						Justification: "Fetch URL content.",
						RiskClass:     "READ",
					},
				},
			},
			{
				AssistantMessage: "The API returned test-payload.",
				ProposedActions:  nil,
			},
		},
		callCount: &callCount,
	}

	// Use a fetcher that bypasses SSRF checks for tests against local httptest servers.
	app := newTestAppWithPlannerAndFetcher(t, planner, agent.NewWebFetcherWithTransport(http.DefaultTransport))
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapAuthToken(t, srv.URL)
	threadID := createThread(t, srv.URL, token)

	// Pre-grant the domain so web_fetch executes inline as READ.
	domain := agent.ExtractDomain(contentServer.URL)
	if err := app.grantDomain(domain, threadID); err != nil {
		t.Fatalf("failed to grant domain: %v", err)
	}

	postMessage(t, srv.URL, token, threadID, "fetch the api")

	// web_fetch to granted domain is READ — no approval needed.
	pending := listApprovals(t, srv.URL, token, "pending")
	if len(pending) != 0 {
		t.Fatalf("expected 0 pending approvals for READ tool, got %d", len(pending))
	}

	msgs := listMessages(t, srv.URL, token, threadID)

	// Tool result messages use role=internal and should be excluded from the API response.
	var foundAssistant bool
	for _, msg := range msgs {
		if msg.Role == "internal" {
			t.Fatalf("internal messages should not be visible via API, got: %v", msg)
		}
		if msg.Role == "system" && strings.Contains(msg.Content, "[tool_result:web_fetch]") {
			t.Fatalf("tool result system messages should no longer appear; they use role=internal now")
		}
		if msg.Role == "assistant" && strings.Contains(msg.Content, "test-payload") {
			foundAssistant = true
		}
	}
	if !foundAssistant {
		t.Fatalf("expected assistant message referencing fetched content")
	}

	// Planner should have been called twice (first for web_fetch, second for final answer).
	if callCount != 2 {
		t.Fatalf("expected planner to be called 2 times, got %d", callCount)
	}
}

func TestWebFetchUngrantedDomainRequiresApprovalAndGrantsDomain(t *testing.T) {
	t.Parallel()

	contentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "fetched-content-from-approval")
	}))
	defer contentServer.Close()

	planner := &staticPlannerFunc{fn: func(_ context.Context, _ agent.PlanRequest) (agent.PlanResult, error) {
		return agent.PlanResult{
			AssistantMessage: "I need to fetch that page for you.",
			ProposedActions: []agent.ProposedAction{
				{
					Tool:          "web_fetch",
					Args:          json.RawMessage(fmt.Sprintf(`{"url":%q}`, contentServer.URL+"/page")),
					Justification: "Fetch URL content.",
					RiskClass:     "READ",
				},
			},
		}, nil
	}}

	app := newTestAppWithPlannerAndFetcher(t, planner, agent.NewWebFetcherWithTransport(http.DefaultTransport))
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapAuthToken(t, srv.URL)
	threadID := createThread(t, srv.URL, token)

	// Domain is NOT pre-granted — web_fetch should become a proposed action.
	postMessage(t, srv.URL, token, threadID, "fetch the page")

	pending := listApprovals(t, srv.URL, token, "pending")
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending approval for ungranted domain, got %d", len(pending))
	}
	if pending[0].Tool != "web_fetch" {
		t.Fatalf("expected pending approval for web_fetch, got %q", pending[0].Tool)
	}

	// Approve the action — this should grant the domain and execute the fetch.
	approveAction(t, srv.URL, token, pending[0].ActionID)

	// Wait for executor to pick it up.
	executed := waitForApprovalStatus(t, srv.URL, token, "executed", pending[0].ActionID, 5*time.Second)
	if len(executed) != 1 {
		t.Fatalf("expected 1 executed approval, got %d", len(executed))
	}

	// Domain should now be granted.
	domain := agent.ExtractDomain(contentServer.URL)
	if !app.isDomainGranted(domain, threadID) {
		t.Fatalf("expected domain %q to be granted after approval", domain)
	}

	// Execution should have persisted the fetch result as a system message.
	msgs := listMessages(t, srv.URL, token, threadID)
	var foundFetchResult bool
	for _, msg := range msgs {
		if msg.Role == "system" && strings.Contains(msg.Content, "fetched-content-from-approval") {
			foundFetchResult = true
		}
	}
	if !foundFetchResult {
		t.Fatalf("expected system message with fetch result after approval")
	}
}

func TestWorkspaceMemoryWriteExecutesInlineWithoutApproval(t *testing.T) {
	t.Parallel()

	callCount := 0
	planner := &callCountPlanner{
		plans: []agent.PlanResult{
			{
				AssistantMessage: "Updating memory.",
				ProposedActions: []agent.ProposedAction{{
					Tool:          "write_file",
					Args:          json.RawMessage(`{"path":"memory/MEMORY.md","content":"- Prefers concise summaries\n"}`),
					Justification: "Persist stable preference.",
					RiskClass:     "HIGH",
				}},
			},
			{
				AssistantMessage: "Saved to memory.",
				ProposedActions:  nil,
			},
		},
		callCount: &callCount,
	}

	app := newTestAppWithPlanner(t, planner)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapAuthToken(t, srv.URL)
	threadID := createThread(t, srv.URL, token)

	postMessage(t, srv.URL, token, threadID, "remember that I prefer concise summaries")

	pending := listApprovals(t, srv.URL, token, "pending")
	if len(pending) != 0 {
		t.Fatalf("expected 0 pending approvals for memory write, got %d", len(pending))
	}

	memoryPath := filepath.Join(app.workspaceRoot, "memory", "MEMORY.md")
	contents, err := os.ReadFile(memoryPath)
	if err != nil {
		t.Fatalf("read memory file: %v", err)
	}
	if !strings.Contains(string(contents), "Prefers concise summaries") {
		t.Fatalf("expected memory write to persist content, got %q", string(contents))
	}

	if callCount != 2 {
		t.Fatalf("expected planner to run twice for inline write flow, got %d", callCount)
	}
}

func TestWorkspaceWritesToProtectedRootFilesRequireApproval(t *testing.T) {
	t.Parallel()

	protectedPaths := []string{"SOUL.md", "LAWS.md"}
	for _, protectedPath := range protectedPaths {
		protectedPath := protectedPath
		t.Run(protectedPath, func(t *testing.T) {
			t.Parallel()

			planner := &staticPlannerFunc{fn: func(_ context.Context, _ agent.PlanRequest) (agent.PlanResult, error) {
				return agent.PlanResult{
					AssistantMessage: "Preparing a policy file update.",
					ProposedActions: []agent.ProposedAction{{
						Tool:          "write_file",
						Args:          json.RawMessage(fmt.Sprintf(`{"path":%q,"content":"updated by test\n"}`, protectedPath)),
						Justification: "Apply requested update.",
						RiskClass:     "READ",
					}},
				}, nil
			}}

			app := newTestAppWithPlanner(t, planner)
			srv := httptest.NewServer(app.Handler())
			defer srv.Close()

			token := bootstrapAuthToken(t, srv.URL)
			threadID := createThread(t, srv.URL, token)

			beforePath := filepath.Join(app.workspaceRoot, filepath.FromSlash(protectedPath))
			before, err := os.ReadFile(beforePath)
			if err != nil {
				t.Fatalf("read before content: %v", err)
			}

			postMessage(t, srv.URL, token, threadID, "update protected file")

			pending := listApprovals(t, srv.URL, token, "pending")
			if len(pending) != 1 {
				t.Fatalf("expected 1 pending approval for %s write, got %d", protectedPath, len(pending))
			}
			if pending[0].Tool != "write_file" {
				t.Fatalf("expected write_file approval, got %q", pending[0].Tool)
			}

			afterUnapproved, err := os.ReadFile(beforePath)
			if err != nil {
				t.Fatalf("read unapproved content: %v", err)
			}
			if string(afterUnapproved) != string(before) {
				t.Fatalf("expected %s to remain unchanged before approval", protectedPath)
			}

			approveAction(t, srv.URL, token, pending[0].ActionID)
			executed := waitForApprovalStatus(t, srv.URL, token, "executed", pending[0].ActionID, 5*time.Second)
			if len(executed) != 1 {
				t.Fatalf("expected approved action to execute for %s", protectedPath)
			}

			afterApproved, err := os.ReadFile(beforePath)
			if err != nil {
				t.Fatalf("read approved content: %v", err)
			}
			if !strings.Contains(string(afterApproved), "updated by test") {
				t.Fatalf("expected approved write to update %s, got %q", protectedPath, string(afterApproved))
			}
		})
	}
}

func TestWorkspaceFileTools(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	ctx := context.Background()

	runTool := func(tool string, args any) (string, error) {
		t.Helper()
		payload, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("marshal args for %s: %v", tool, err)
		}
		return app.executeInlineReadTool(ctx, "thr_test", agent.ProposedAction{
			Tool: tool,
			Args: payload,
		})
	}

	if output, err := runTool("write_file", map[string]string{
		"path":    "scratch/note.txt",
		"content": "line-one\n",
	}); err != nil {
		t.Fatalf("write_file failed: %v (output=%q)", err, output)
	}

	if output, err := runTool("append_file", map[string]string{
		"path":    "scratch/note.txt",
		"content": "line-two",
	}); err != nil {
		t.Fatalf("append_file failed: %v (output=%q)", err, output)
	}

	readOutput, err := runTool("read_file", map[string]string{"path": "scratch/note.txt"})
	if err != nil {
		t.Fatalf("read_file failed: %v (output=%q)", err, readOutput)
	}
	if !strings.Contains(readOutput, "line-one\nline-two\n") {
		t.Fatalf("unexpected read_file output: %q", readOutput)
	}

	listingOutput, err := runTool("list_dir", map[string]string{"path": "scratch"})
	if err != nil {
		t.Fatalf("list_dir failed: %v (output=%q)", err, listingOutput)
	}
	var listing listDirResult
	if err := json.Unmarshal([]byte(listingOutput), &listing); err != nil {
		t.Fatalf("decode list_dir output: %v (output=%q)", err, listingOutput)
	}
	var sawNote bool
	for _, entry := range listing.Entries {
		if entry.Name == "note.txt" && entry.Type == "file" {
			sawNote = true
			break
		}
	}
	if !sawNote {
		t.Fatalf("expected note.txt in directory listing, got %#v", listing.Entries)
	}

	if output, err := runTool("read_file", map[string]string{"path": "../escape.txt"}); err == nil {
		t.Fatalf("expected traversal read_file to fail, got output=%q", output)
	}

	oversized := strings.Repeat("x", int(workspaceMaxFileBytes)+1)
	if output, err := runTool("write_file", map[string]string{"path": "scratch/too-big.txt", "content": oversized}); err == nil {
		t.Fatalf("expected write_file to enforce per-file quota, got output=%q", output)
	}

	app.workspaceQuotaMu.Lock()
	app.workspaceTotalBytes = workspaceMaxTotalBytes
	app.workspaceQuotaMu.Unlock()

	if output, err := runTool("write_file", map[string]string{"path": "scratch/overflow.txt", "content": "x"}); err == nil {
		t.Fatalf("expected write_file to enforce workspace quota, got output=%q", output)
	}
}

func TestWorkspaceFileToolsRejectSymlinkEscape(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	ctx := context.Background()

	runTool := func(tool string, args any) (string, error) {
		t.Helper()
		payload, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("marshal args for %s: %v", tool, err)
		}
		return app.executeInlineReadTool(ctx, "thr_test", agent.ProposedAction{Tool: tool, Args: payload})
	}

	outsidePath := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outsidePath, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	symlinkPath := filepath.Join(app.workspaceRoot, "scratch", "escape-link.txt")
	if err := os.MkdirAll(filepath.Dir(symlinkPath), 0o755); err != nil {
		t.Fatalf("create symlink parent dir: %v", err)
	}
	if err := os.Symlink(outsidePath, symlinkPath); err != nil {
		t.Skipf("symlink unsupported in this environment: %v", err)
	}

	if output, err := runTool("read_file", map[string]string{"path": "scratch/escape-link.txt"}); err == nil {
		t.Fatalf("expected read_file symlink escape rejection, got output=%q", output)
	}
	if output, err := runTool("write_file", map[string]string{"path": "scratch/escape-link.txt", "content": "overwrite"}); err == nil {
		t.Fatalf("expected write_file symlink escape rejection, got output=%q", output)
	}
}

func TestSchedulerWorkerDispatchesDueScheduleAfterRestart(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "pincer-test.db")
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")

	planner := &staticPlannerFunc{fn: func(_ context.Context, req agent.PlanRequest) (agent.PlanResult, error) {
		if req.UserMessage == "restart schedule goal" {
			return agent.PlanResult{AssistantMessage: "Scheduled restart run complete."}, nil
		}
		return agent.PlanResult{AssistantMessage: "ok"}, nil
	}}

	app1, err := New(AppConfig{
		DBPath:                  dbPath,
		WorkspaceRoot:           workspaceRoot,
		Planner:                 planner,
		DisableBackgroundWorker: true,
	})
	if err != nil {
		t.Fatalf("new app1: %v", err)
	}

	srv1 := httptest.NewServer(app1.Handler())
	token := bootstrapAuthToken(t, srv1.URL)
	schedulesClient := protocolv1connect.NewSchedulesServiceClient(connectHTTPClient(token), srv1.URL)

	createResp, err := schedulesClient.CreateSchedule(context.Background(), connect.NewRequest(&protocolv1.CreateScheduleRequest{
		Name:        "restart schedule goal",
		TriggerKind: protocolv1.ScheduleTriggerKind_SCHEDULE_TRIGGER_INTERVAL,
		TriggerSpec: "15m",
		Timezone:    "UTC",
	}))
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	scheduleID := createResp.Msg.GetItem().GetScheduleId()

	pastDue := time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339Nano)
	nowStr := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := app1.db.Exec(`
		UPDATE schedules
		SET next_run_at = ?, updated_at = ?
		WHERE schedule_id = ?
	`, pastDue, nowStr, scheduleID); err != nil {
		t.Fatalf("set schedule due in the past: %v", err)
	}

	var preRestartJobs int
	if err := app1.db.QueryRow(`
		SELECT COUNT(*)
		FROM jobs
		WHERE trigger_type = ? AND trigger_source_id = ?
	`, protocolv1.TriggerType_SCHEDULE_WAKEUP.String(), scheduleID).Scan(&preRestartJobs); err != nil {
		t.Fatalf("count jobs before restart: %v", err)
	}
	if preRestartJobs != 0 {
		t.Fatalf("expected no schedule jobs before restart, got %d", preRestartJobs)
	}

	srv1.Close()
	if err := app1.Close(); err != nil {
		t.Fatalf("close app1: %v", err)
	}

	app2, err := New(AppConfig{
		DBPath:        dbPath,
		WorkspaceRoot: workspaceRoot,
		Planner:       planner,
	})
	if err != nil {
		t.Fatalf("new app2: %v", err)
	}
	t.Cleanup(func() {
		_ = app2.Close()
	})

	waitForCondition(t, 8*time.Second, func() bool {
		var total int
		var completed int
		err := app2.db.QueryRow(`
			SELECT COUNT(*), SUM(CASE WHEN status = ? THEN 1 ELSE 0 END)
			FROM jobs
			WHERE trigger_type = ? AND trigger_source_id = ?
		`, jobStatusCompleted, protocolv1.TriggerType_SCHEDULE_WAKEUP.String(), scheduleID).Scan(&total, &completed)
		if err != nil {
			return false
		}
		return total == 1 && completed == 1
	}, "schedule wakeup dispatched after restart")

	var wakeupCount int
	if err := app2.db.QueryRow(`SELECT COUNT(*) FROM wakeup_events WHERE schedule_id = ?`, scheduleID).Scan(&wakeupCount); err != nil {
		t.Fatalf("count wakeup events: %v", err)
	}
	if wakeupCount != 1 {
		t.Fatalf("expected exactly 1 wakeup event, got %d", wakeupCount)
	}
}

// staticPlannerFunc wraps a function as a Planner.
type staticPlannerFunc struct {
	fn func(context.Context, agent.PlanRequest) (agent.PlanResult, error)
}

func (p *staticPlannerFunc) Plan(ctx context.Context, req agent.PlanRequest) (agent.PlanResult, error) {
	return p.fn(ctx, req)
}

// callCountPlanner returns successive plan results, cycling through the list.
type callCountPlanner struct {
	plans     []agent.PlanResult
	callCount *int
}

func (p *callCountPlanner) Plan(_ context.Context, _ agent.PlanRequest) (agent.PlanResult, error) {
	idx := *p.callCount
	*p.callCount++
	if idx >= len(p.plans) {
		idx = len(p.plans) - 1
	}
	return p.plans[idx], nil
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
	app, err := New(AppConfig{
		DBPath:        dbPath,
		WorkspaceRoot: filepath.Join(t.TempDir(), "workspace"),
	})
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
		DBPath:        filepath.Join(t.TempDir(), "pincer-test.db"),
		WorkspaceRoot: filepath.Join(t.TempDir(), "workspace"),
		Planner:       planner,
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() {
		_ = app.Close()
	})
	return app
}

func newTestAppWithPlannerAndFetcher(t *testing.T, planner agent.Planner, fetcher *agent.WebFetcher) *App {
	t.Helper()
	app, err := New(AppConfig{
		DBPath:        filepath.Join(t.TempDir(), "pincer-test.db"),
		WorkspaceRoot: filepath.Join(t.TempDir(), "workspace"),
		Planner:       planner,
		WebFetcher:    fetcher,
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

func waitForCondition(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for condition: %s", message)
		}
		time.Sleep(20 * time.Millisecond)
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

func TestInlineReadToolCallIDCorrelation(t *testing.T) {
	t.Parallel()

	contentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "hello from server")
	}))
	defer contentServer.Close()

	callCount := 0
	planner := &callCountPlanner{
		plans: []agent.PlanResult{
			{
				AssistantMessage: "Fetching.",
				ProposedActions: []agent.ProposedAction{
					{
						Tool:          "web_fetch",
						Args:          json.RawMessage(fmt.Sprintf(`{"url":%q}`, contentServer.URL)),
						Justification: "Fetch content.",
						RiskClass:     "READ",
					},
				},
			},
			{
				AssistantMessage: "Done.",
				ProposedActions:  nil,
			},
		},
		callCount: &callCount,
	}

	app := newTestAppWithPlannerAndFetcher(t, planner, agent.NewWebFetcherWithTransport(http.DefaultTransport))
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapAuthToken(t, srv.URL)
	threadID := createThread(t, srv.URL, token)

	domain := agent.ExtractDomain(contentServer.URL)
	if err := app.grantDomain(domain, threadID); err != nil {
		t.Fatalf("failed to grant domain: %v", err)
	}

	// Collect all events from the StartTurn stream.
	client := protocolv1connect.NewTurnsServiceClient(connectHTTPClient(token), srv.URL)
	stream, err := client.StartTurn(context.Background(), connect.NewRequest(&protocolv1.StartTurnRequest{
		ThreadId:    threadID,
		UserText:    "fetch it",
		TriggerType: protocolv1.TriggerType_CHAT_MESSAGE,
	}))
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}

	var plannedIDs []string
	var executionToolCallIDs []string
	for stream.Receive() {
		event := stream.Msg()
		if p := event.GetToolCallPlanned(); p != nil {
			plannedIDs = append(plannedIDs, p.GetToolCallId())
			if p.GetToolName() != "web_fetch" {
				t.Errorf("expected tool_name=web_fetch, got %s", p.GetToolName())
			}
			if p.GetRiskClass() != protocolv1.RiskClass_READ {
				t.Errorf("expected risk_class=READ, got %s", p.GetRiskClass())
			}
		}
		if s := event.GetToolExecutionStarted(); s != nil {
			executionToolCallIDs = append(executionToolCallIDs, s.GetToolCallId())
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}

	// ToolCallPlanned.tool_call_id must match ToolExecutionStarted.tool_call_id.
	if len(plannedIDs) == 0 {
		t.Fatal("expected at least one ToolCallPlanned event")
	}
	if len(executionToolCallIDs) == 0 {
		t.Fatal("expected at least one ToolExecutionStarted event")
	}
	if plannedIDs[0] != executionToolCallIDs[0] {
		t.Fatalf("ID mismatch: ToolCallPlanned.tool_call_id=%s, ToolExecutionStarted.tool_call_id=%s", plannedIDs[0], executionToolCallIDs[0])
	}
}

func TestApprovalPathToolCallIDCorrelation(t *testing.T) {
	t.Parallel()

	planner := stubPlanner{
		result: agent.PlanResult{
			AssistantMessage: "Will run bash.",
			ProposedActions: []agent.ProposedAction{
				{
					Tool:          "run_bash",
					Args:          json.RawMessage(`{"command":"echo hi"}`),
					Justification: "Test.",
					RiskClass:     "HIGH",
				},
			},
		},
	}

	app := newTestAppWithPlanner(t, planner)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapAuthToken(t, srv.URL)
	threadID := createThread(t, srv.URL, token)

	client := protocolv1connect.NewTurnsServiceClient(connectHTTPClient(token), srv.URL)
	stream, err := client.StartTurn(context.Background(), connect.NewRequest(&protocolv1.StartTurnRequest{
		ThreadId:    threadID,
		UserText:    "run something",
		TriggerType: protocolv1.TriggerType_CHAT_MESSAGE,
	}))
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}

	var plannedIDs []string
	var proposedActionIDs []string
	for stream.Receive() {
		event := stream.Msg()
		if p := event.GetToolCallPlanned(); p != nil {
			plannedIDs = append(plannedIDs, p.GetToolCallId())
		}
		if a := event.GetProposedActionCreated(); a != nil {
			proposedActionIDs = append(proposedActionIDs, a.GetActionId())
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}

	// ToolCallPlanned.tool_call_id must match ProposedActionCreated.action_id.
	if len(plannedIDs) == 0 {
		t.Fatal("expected at least one ToolCallPlanned event")
	}
	if len(proposedActionIDs) == 0 {
		t.Fatal("expected at least one ProposedActionCreated event")
	}
	if plannedIDs[0] != proposedActionIDs[0] {
		t.Fatalf("ID mismatch: ToolCallPlanned.tool_call_id=%s, ProposedActionCreated.action_id=%s", plannedIDs[0], proposedActionIDs[0])
	}
}

func TestInlineReadToolCallArgsPersisted(t *testing.T) {
	t.Parallel()

	contentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "content")
	}))
	defer contentServer.Close()

	fetchURL := contentServer.URL
	callCount := 0
	var capturedHistory []agent.Message
	planner := &staticPlannerFunc{fn: func(_ context.Context, req agent.PlanRequest) (agent.PlanResult, error) {
		callCount++
		if callCount == 1 {
			return agent.PlanResult{
				AssistantMessage: "Fetching.",
				ProposedActions: []agent.ProposedAction{
					{
						Tool:          "web_fetch",
						Args:          json.RawMessage(fmt.Sprintf(`{"url":%q}`, fetchURL)),
						Justification: "Fetch.",
						RiskClass:     "READ",
					},
				},
			}, nil
		}
		capturedHistory = req.History
		return agent.PlanResult{AssistantMessage: "Done.", ProposedActions: nil}, nil
	}}

	app := newTestAppWithPlannerAndFetcher(t, planner, agent.NewWebFetcherWithTransport(http.DefaultTransport))
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapAuthToken(t, srv.URL)
	threadID := createThread(t, srv.URL, token)

	domain := agent.ExtractDomain(contentServer.URL)
	if err := app.grantDomain(domain, threadID); err != nil {
		t.Fatalf("failed to grant domain: %v", err)
	}

	postMessage(t, srv.URL, token, threadID, "fetch it")

	// The planner should have seen a [tool_call:web_fetch] message in history on the second call.
	var foundToolCall, foundToolResult bool
	for _, msg := range capturedHistory {
		if strings.Contains(msg.Content, "[tool_call:web_fetch]") && strings.Contains(msg.Content, fetchURL) {
			foundToolCall = true
		}
		if strings.Contains(msg.Content, "[tool_result:web_fetch]") {
			foundToolResult = true
		}
	}
	if !foundToolCall {
		t.Fatalf("expected planner history to contain [tool_call:web_fetch] with args, got: %v", capturedHistory)
	}
	if !foundToolResult {
		t.Fatalf("expected planner history to contain [tool_result:web_fetch], got: %v", capturedHistory)
	}
}

func TestImageDescribeInlineExecution(t *testing.T) {
	t.Parallel()

	// Mock vision API server.
	visionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": "A photo of a cat sitting on a windowsill."}},
			},
		})
	}))
	defer visionServer.Close()

	imageURL := "https://example.com/cat.jpg"
	callCount := 0
	var capturedHistory []agent.Message
	planner := &staticPlannerFunc{fn: func(_ context.Context, req agent.PlanRequest) (agent.PlanResult, error) {
		callCount++
		if callCount == 1 {
			return agent.PlanResult{
				AssistantMessage: "Analyzing image.",
				ProposedActions: []agent.ProposedAction{
					{
						Tool:          "image_describe",
						Args:          json.RawMessage(fmt.Sprintf(`{"url":%q}`, imageURL)),
						Justification: "Describe image.",
						RiskClass:     "READ",
					},
				},
			}, nil
		}
		capturedHistory = req.History
		return agent.PlanResult{AssistantMessage: "It's a cat on a windowsill.", ProposedActions: nil}, nil
	}}

	app, err := New(AppConfig{
		DBPath:         filepath.Join(t.TempDir(), "pincer-test.db"),
		WorkspaceRoot:  filepath.Join(t.TempDir(), "workspace"),
		Planner:        planner,
		ImageDescriber: agent.NewImageDescriber("test-key", visionServer.URL),
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapAuthToken(t, srv.URL)
	threadID := createThread(t, srv.URL, token)

	// Grant the image domain so image_describe executes inline (not escalated to EXFILTRATION).
	if err := app.grantDomain("example.com", threadID); err != nil {
		t.Fatalf("failed to grant domain: %v", err)
	}

	postMessage(t, srv.URL, token, threadID, "what's in this image?")

	var foundToolCall, foundToolResult bool
	for _, msg := range capturedHistory {
		if strings.Contains(msg.Content, "[tool_call:image_describe]") && strings.Contains(msg.Content, imageURL) {
			foundToolCall = true
		}
		if strings.Contains(msg.Content, "[tool_result:image_describe]") && strings.Contains(msg.Content, "cat") {
			foundToolResult = true
		}
	}
	if !foundToolCall {
		t.Fatalf("expected planner history to contain [tool_call:image_describe], got: %v", capturedHistory)
	}
	if !foundToolResult {
		t.Fatalf("expected planner history to contain [tool_result:image_describe] with vision output, got: %v", capturedHistory)
	}
}

func TestHeartbeatWorkerCreatesSystemThreadAndRunsHeartbeatTurns(t *testing.T) {
	t.Parallel()

	app, err := New(AppConfig{
		DBPath:                 filepath.Join(t.TempDir(), "pincer-test.db"),
		WorkspaceRoot:          filepath.Join(t.TempDir(), "workspace"),
		Planner:                stubPlanner{result: agent.PlanResult{AssistantMessage: "Heartbeat update."}},
		HeartbeatEnabled:       true,
		HeartbeatInterval:      25 * time.Millisecond,
		ActionExecutorInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	waitForCondition(t, 2*time.Second, func() bool {
		var count int
		if err := app.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE thread_id = ? AND role = 'assistant'`, heartbeatThreadID).Scan(&count); err != nil {
			return false
		}
		return count > 0
	}, "heartbeat assistant message")

	var channel, title string
	if err := app.db.QueryRow(`SELECT channel, title FROM threads WHERE thread_id = ?`, heartbeatThreadID).Scan(&channel, &title); err != nil {
		t.Fatalf("query heartbeat thread: %v", err)
	}
	if channel != "system" {
		t.Fatalf("expected heartbeat thread channel system, got %q", channel)
	}
	if title != "Heartbeat" {
		t.Fatalf("expected heartbeat thread title Heartbeat, got %q", title)
	}

	events, err := app.listThreadEvents(context.Background(), heartbeatThreadID, 0, 200)
	if err != nil {
		t.Fatalf("list heartbeat thread events: %v", err)
	}
	var foundTurnStarted bool
	for _, event := range events {
		if started := event.GetTurnStarted(); started != nil && started.GetTriggerType() == protocolv1.TriggerType_HEARTBEAT {
			foundTurnStarted = true
			break
		}
	}
	if !foundTurnStarted {
		t.Fatalf("expected at least one heartbeat TriggerType in TurnStarted events")
	}
}

func TestHeartbeatOKDoesNotSurfaceVisibleMessages(t *testing.T) {
	t.Parallel()

	var plannerCalls atomic.Int32
	planner := &staticPlannerFunc{fn: func(_ context.Context, req agent.PlanRequest) (agent.PlanResult, error) {
		plannerCalls.Add(1)
		if !strings.Contains(req.UserMessage, "HEARTBEAT_OK") {
			t.Fatalf("expected heartbeat prompt instructions in user message, got %q", req.UserMessage)
		}
		if !strings.Contains(req.UserMessage, "jobs_list") || !strings.Contains(req.UserMessage, "schedule_list") {
			t.Fatalf("expected heartbeat prompt to mention jobs_list and schedule_list, got %q", req.UserMessage)
		}
		return agent.PlanResult{AssistantMessage: heartbeatSilentMarker}, nil
	}}

	app, err := New(AppConfig{
		DBPath:                 filepath.Join(t.TempDir(), "pincer-test.db"),
		WorkspaceRoot:          filepath.Join(t.TempDir(), "workspace"),
		Planner:                planner,
		HeartbeatEnabled:       true,
		HeartbeatInterval:      25 * time.Millisecond,
		ActionExecutorInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	waitForCondition(t, 2*time.Second, func() bool {
		return plannerCalls.Load() > 0
	}, "heartbeat planner call")

	var visibleCount int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE thread_id = ? AND role != 'internal'`, heartbeatThreadID).Scan(&visibleCount); err != nil {
		t.Fatalf("count heartbeat visible messages: %v", err)
	}
	if visibleCount != 0 {
		t.Fatalf("expected no visible heartbeat messages for HEARTBEAT_OK, got %d", visibleCount)
	}

	var internalCount int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE thread_id = ? AND role = 'internal'`, heartbeatThreadID).Scan(&internalCount); err != nil {
		t.Fatalf("count heartbeat internal messages: %v", err)
	}
	if internalCount != 0 {
		t.Fatalf("expected heartbeat no-op runs to leave no persisted internal messages, got %d", internalCount)
	}
}

func TestHeartbeatSilentTurnsDoNotAccumulatePromptHistory(t *testing.T) {
	t.Parallel()

	var plannerCalls atomic.Int32
	var sawPromptInHistory atomic.Bool
	planner := &staticPlannerFunc{fn: func(_ context.Context, req agent.PlanRequest) (agent.PlanResult, error) {
		if plannerCalls.Add(1) > 1 {
			for _, msg := range req.History {
				if strings.HasPrefix(msg.Content, heartbeatPromptPrefix) {
					sawPromptInHistory.Store(true)
					break
				}
			}
		}
		return agent.PlanResult{AssistantMessage: heartbeatSilentMarker}, nil
	}}

	app, err := New(AppConfig{
		DBPath:                 filepath.Join(t.TempDir(), "pincer-test.db"),
		WorkspaceRoot:          filepath.Join(t.TempDir(), "workspace"),
		Planner:                planner,
		HeartbeatEnabled:       true,
		HeartbeatInterval:      25 * time.Millisecond,
		ActionExecutorInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	waitForCondition(t, 2*time.Second, func() bool {
		return plannerCalls.Load() >= 3
	}, "multiple heartbeat planner calls")

	if sawPromptInHistory.Load() {
		t.Fatalf("expected heartbeat prompt messages to be excluded from planner history on subsequent runs")
	}

	var promptMessageCount int
	if err := app.db.QueryRow(`
		SELECT COUNT(*)
		FROM messages
		WHERE thread_id = ? AND role = 'internal' AND content LIKE ?
	`, heartbeatThreadID, heartbeatPromptPrefix+"%").Scan(&promptMessageCount); err != nil {
		t.Fatalf("count persisted heartbeat prompt messages: %v", err)
	}
	if promptMessageCount != 0 {
		t.Fatalf("expected no persisted heartbeat prompt messages, got %d", promptMessageCount)
	}
}

func TestHeartbeatRiskyActionsStayApprovalGated(t *testing.T) {
	t.Parallel()

	planner := stubPlanner{result: agent.PlanResult{
		AssistantMessage: "I need approval to run a command.",
		ProposedActions: []agent.ProposedAction{{
			Tool:          "run_bash",
			Args:          json.RawMessage(`{"command":"echo from heartbeat"}`),
			Justification: "Needs external side effect",
			RiskClass:     "HIGH",
		}},
	}}

	app, err := New(AppConfig{
		DBPath:                 filepath.Join(t.TempDir(), "pincer-test.db"),
		WorkspaceRoot:          filepath.Join(t.TempDir(), "workspace"),
		Planner:                planner,
		HeartbeatEnabled:       true,
		HeartbeatInterval:      25 * time.Millisecond,
		ActionExecutorInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	waitForCondition(t, 2*time.Second, func() bool {
		var pending int
		if err := app.db.QueryRow(`
			SELECT COUNT(*)
			FROM proposed_actions
			WHERE source_id = ? AND tool = 'run_bash' AND status = 'PENDING'
		`, heartbeatThreadID).Scan(&pending); err != nil {
			return false
		}
		return pending > 0
	}, "heartbeat pending approval")

	var executed int
	if err := app.db.QueryRow(`
		SELECT COUNT(*)
		FROM proposed_actions
		WHERE source_id = ? AND tool = 'run_bash' AND status = 'EXECUTED'
	`, heartbeatThreadID).Scan(&executed); err != nil {
		t.Fatalf("count executed heartbeat approvals: %v", err)
	}
	if executed != 0 {
		t.Fatalf("expected zero executed heartbeat actions without approval, got %d", executed)
	}
}

func TestRunningJobsAreFailedOnStartup(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "pincer-test.db")
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")

	app, err := New(AppConfig{
		DBPath:                  dbPath,
		WorkspaceRoot:           workspaceRoot,
		DisableBackgroundWorker: true,
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	jobID := "job_restart_case"
	threadID := "thr_restart_case"
	if _, err := app.db.Exec(`
		INSERT INTO threads(thread_id, user_id, channel, created_at, title, updated_at)
		VALUES(?, ?, 'system', ?, 'Job: restart test', ?)
	`, threadID, defaultOwnerID, now, now); err != nil {
		t.Fatalf("insert thread: %v", err)
	}
	if _, err := app.db.Exec(`
		INSERT INTO jobs(
			job_id, user_id, goal, status, thread_id, origin_thread_id,
			trigger_type, trigger_source_id, max_tool_steps, max_wall_time_ms,
			current_turn_id, started_at, last_error, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, jobID, defaultOwnerID, "restart me", jobStatusRunning, threadID, threadID,
		protocolv1.TriggerType_JOB_WAKEUP.String(), "test", defaultJobMaxToolSteps,
		uint64(defaultJobMaxWallTime/time.Millisecond), "turn_restart", now, "", now, now); err != nil {
		t.Fatalf("insert running job: %v", err)
	}

	if err := app.Close(); err != nil {
		t.Fatalf("close app: %v", err)
	}

	appRestarted, err := New(AppConfig{
		DBPath:                  dbPath,
		WorkspaceRoot:           workspaceRoot,
		DisableBackgroundWorker: true,
	})
	if err != nil {
		t.Fatalf("new app restarted: %v", err)
	}
	t.Cleanup(func() { _ = appRestarted.Close() })

	var status string
	var lastError string
	if err := appRestarted.db.QueryRow(`
		SELECT status, last_error
		FROM jobs
		WHERE job_id = ?
	`, jobID).Scan(&status, &lastError); err != nil {
		t.Fatalf("query restarted job: %v", err)
	}
	if status != jobStatusFailed {
		t.Fatalf("expected restarted running job to be %q, got %q", jobStatusFailed, status)
	}
	if lastError != "failed_restart" {
		t.Fatalf("expected restarted job last_error to be %q, got %q", "failed_restart", lastError)
	}

	var auditCount int
	if err := appRestarted.db.QueryRow(`
		SELECT COUNT(*)
		FROM audit_log
		WHERE event_type = 'job_failed_restart' AND entity_id = ?
	`, jobID).Scan(&auditCount); err != nil {
		t.Fatalf("query restart audit: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("expected 1 job_failed_restart audit event, got %d", auditCount)
	}
}

func TestWorkQueueClaimsChatBeforeHeartbeatByPriority(t *testing.T) {
	t.Parallel()

	app, err := New(AppConfig{
		DBPath:                  filepath.Join(t.TempDir(), "pincer-test.db"),
		WorkspaceRoot:           filepath.Join(t.TempDir(), "workspace"),
		DisableBackgroundWorker: true,
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	now := time.Now().UTC().Format(time.RFC3339Nano)
	chatThreadID := "thr_work_queue_chat"
	heartbeatThreadID := "thr_work_queue_heartbeat"
	if _, err := app.db.Exec(`
		INSERT INTO threads(thread_id, user_id, channel, created_at, title, updated_at)
		VALUES(?, ?, 'ios', ?, 'Chat Queue', ?)
	`, chatThreadID, defaultOwnerID, now, now); err != nil {
		t.Fatalf("insert chat thread: %v", err)
	}
	if _, err := app.db.Exec(`
		INSERT INTO threads(thread_id, user_id, channel, created_at, title, updated_at)
		VALUES(?, ?, 'system', ?, 'Heartbeat Queue', ?)
	`, heartbeatThreadID, defaultOwnerID, now, now); err != nil {
		t.Fatalf("insert heartbeat thread: %v", err)
	}

	if _, err := app.enqueueWorkItem(context.Background(), workItemInput{
		Kind:        workItemKindHeartbeat,
		TriggerType: protocolv1.TriggerType_HEARTBEAT,
		ThreadID:    heartbeatThreadID,
		TurnID:      newID("turn"),
		Prompt:      "heartbeat prompt",
		SourceID:    heartbeatThreadID,
	}); err != nil {
		t.Fatalf("enqueue heartbeat item: %v", err)
	}
	if _, err := app.enqueueWorkItem(context.Background(), workItemInput{
		Kind:        workItemKindChat,
		TriggerType: protocolv1.TriggerType_CHAT_MESSAGE,
		ThreadID:    chatThreadID,
		TurnID:      newID("turn"),
		Prompt:      "chat prompt",
		SourceID:    chatThreadID,
	}); err != nil {
		t.Fatalf("enqueue chat item: %v", err)
	}

	claimed, err := app.claimNextWorkItem(context.Background())
	if err != nil {
		t.Fatalf("claim next work item: %v", err)
	}
	if claimed == nil {
		t.Fatalf("expected claimed work item")
	}
	if claimed.Kind != workItemKindChat {
		t.Fatalf("expected chat item to be claimed first, got kind=%q", claimed.Kind)
	}
}

func TestWorkQueueProcessingItemsRequeuedOnStartup(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "pincer-test.db")
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")

	app, err := New(AppConfig{
		DBPath:                  dbPath,
		WorkspaceRoot:           workspaceRoot,
		DisableBackgroundWorker: true,
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	threadID := "thr_work_queue_restart"
	if _, err := app.db.Exec(`
		INSERT INTO threads(thread_id, user_id, channel, created_at, title, updated_at, active_turn_id)
		VALUES(?, ?, 'ios', ?, 'Restart Queue', ?, '')
	`, threadID, defaultOwnerID, now, now); err != nil {
		t.Fatalf("insert thread: %v", err)
	}

	queued, err := app.enqueueWorkItem(context.Background(), workItemInput{
		Kind:        workItemKindChat,
		TriggerType: protocolv1.TriggerType_CHAT_MESSAGE,
		ThreadID:    threadID,
		TurnID:      "turn_restart",
		Prompt:      "resume this",
		SourceID:    threadID,
	})
	if err != nil {
		t.Fatalf("enqueue work item: %v", err)
	}
	if _, err := app.claimNextWorkItem(context.Background()); err != nil {
		t.Fatalf("claim work item: %v", err)
	}

	if err := app.Close(); err != nil {
		t.Fatalf("close app: %v", err)
	}

	restarted, err := New(AppConfig{
		DBPath:                  dbPath,
		WorkspaceRoot:           workspaceRoot,
		DisableBackgroundWorker: true,
	})
	if err != nil {
		t.Fatalf("new restarted app: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })

	var status string
	var startedAt string
	var activeTurn string
	if err := restarted.db.QueryRow(`
		SELECT status, started_at
		FROM work_items
		WHERE work_item_id = ?
	`, queued.WorkItemID).Scan(&status, &startedAt); err != nil {
		t.Fatalf("query work item: %v", err)
	}
	if status != workItemStatusPending {
		t.Fatalf("expected restarted work item status %q, got %q", workItemStatusPending, status)
	}
	if startedAt != "" {
		t.Fatalf("expected restarted work item started_at to be cleared, got %q", startedAt)
	}

	if err := restarted.db.QueryRow(`SELECT active_turn_id FROM threads WHERE thread_id = ?`, threadID).Scan(&activeTurn); err != nil {
		t.Fatalf("query thread lock: %v", err)
	}
	if activeTurn != "" {
		t.Fatalf("expected thread active_turn_id to be cleared after restart, got %q", activeTurn)
	}
}

func TestQueuedJobWorkItemKeepsApprovalConveyor(t *testing.T) {
	t.Parallel()

	planner := &staticPlannerFunc{fn: func(_ context.Context, req agent.PlanRequest) (agent.PlanResult, error) {
		if req.UserMessage == "queued job needs approval" {
			return agent.PlanResult{
				AssistantMessage: "Job requires approval.",
				ProposedActions: []agent.ProposedAction{{
					Tool:          "run_bash",
					Args:          json.RawMessage(`{"command":"echo queued job"}`),
					Justification: "External side effect",
					RiskClass:     "HIGH",
				}},
			}, nil
		}
		return agent.PlanResult{AssistantMessage: "ok"}, nil
	}}

	app, err := New(AppConfig{
		DBPath:                  filepath.Join(t.TempDir(), "pincer-test.db"),
		WorkspaceRoot:           filepath.Join(t.TempDir(), "workspace"),
		Planner:                 planner,
		DisableBackgroundWorker: true,
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	job, err := app.createJob(context.Background(), createJobInput{
		Goal:          "queued job needs approval",
		TriggerType:   protocolv1.TriggerType_JOB_WAKEUP,
		TriggerSource: "queue-test",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	app.processJobQueueOnce()
	app.processWorkQueueOnce()

	waitForCondition(t, 3*time.Second, func() bool {
		item, getErr := app.getJobByID(context.Background(), job.JobID)
		if getErr != nil {
			return false
		}
		return item.Status == jobStatusWaitingApproval
	}, "queued job waiting approval")

	var pending int
	if err := app.db.QueryRow(`
		SELECT COUNT(*)
		FROM proposed_actions
		WHERE source = 'job' AND source_id = ? AND status = 'PENDING'
	`, job.ThreadID).Scan(&pending); err != nil {
		t.Fatalf("count pending job approvals: %v", err)
	}
	if pending == 0 {
		t.Fatalf("expected pending approval for queued job")
	}

	var executed int
	if err := app.db.QueryRow(`
		SELECT COUNT(*)
		FROM proposed_actions
		WHERE source = 'job' AND source_id = ? AND status = 'EXECUTED'
	`, job.ThreadID).Scan(&executed); err != nil {
		t.Fatalf("count executed job approvals: %v", err)
	}
	if executed != 0 {
		t.Fatalf("expected zero executed actions before approval, got %d", executed)
	}
}

func TestWorkQueueKeepsSchedulePendingWhenThreadBusy(t *testing.T) {
	t.Parallel()

	app, err := New(AppConfig{
		DBPath:                  filepath.Join(t.TempDir(), "pincer-test.db"),
		WorkspaceRoot:           filepath.Join(t.TempDir(), "workspace"),
		DisableBackgroundWorker: true,
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	now := time.Now().UTC().Format(time.RFC3339Nano)
	threadID := "thr_work_queue_schedule_busy"
	if _, err := app.db.Exec(`
		INSERT INTO threads(thread_id, user_id, channel, created_at, title, updated_at, active_turn_id)
		VALUES(?, ?, 'system', ?, 'Schedule Busy', ?, ?)
	`, threadID, defaultOwnerID, now, now, "turn_busy"); err != nil {
		t.Fatalf("insert thread: %v", err)
	}

	queued, err := app.enqueueWorkItem(context.Background(), workItemInput{
		Kind:        workItemKindSchedule,
		TriggerType: protocolv1.TriggerType_SCHEDULE_WAKEUP,
		ThreadID:    threadID,
		TurnID:      "turn_schedule",
		Prompt:      "scheduled goal",
		SourceID:    "schedule_123",
		JobID:       "job_schedule",
	})
	if err != nil {
		t.Fatalf("enqueue schedule work item: %v", err)
	}

	claimed, err := app.claimNextWorkItem(context.Background())
	if err != nil {
		t.Fatalf("claim next work item: %v", err)
	}
	if claimed != nil {
		t.Fatalf("expected no claimed item while thread is busy, got %q", claimed.WorkItemID)
	}

	var status string
	if err := app.db.QueryRow(`SELECT status FROM work_items WHERE work_item_id = ?`, queued.WorkItemID).Scan(&status); err != nil {
		t.Fatalf("query work item status: %v", err)
	}
	if status != workItemStatusPending {
		t.Fatalf("expected schedule work item to remain pending, got %q", status)
	}
}

func TestJobRunnerStopsRequeueAfterQueuedWorkItemFailure(t *testing.T) {
	t.Parallel()

	planner := &staticPlannerFunc{fn: func(_ context.Context, req agent.PlanRequest) (agent.PlanResult, error) {
		if req.UserMessage == "queued job always fails" {
			return agent.PlanResult{}, errors.New("planner failed deterministically")
		}
		return agent.PlanResult{AssistantMessage: "ok"}, nil
	}}

	app, err := New(AppConfig{
		DBPath:                  filepath.Join(t.TempDir(), "pincer-test.db"),
		WorkspaceRoot:           filepath.Join(t.TempDir(), "workspace"),
		Planner:                 planner,
		DisableBackgroundWorker: true,
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	job, err := app.createJob(context.Background(), createJobInput{
		Goal:          "queued job always fails",
		TriggerType:   protocolv1.TriggerType_JOB_WAKEUP,
		TriggerSource: "failure-test",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	for i := 0; i < 3; i++ {
		app.processJobQueueOnce()
		app.processWorkQueueOnce()
		time.Sleep(40 * time.Millisecond)
	}

	waitForCondition(t, 3*time.Second, func() bool {
		item, getErr := app.getJobByID(context.Background(), job.JobID)
		if getErr != nil {
			return false
		}
		return item.Status == jobStatusFailed
	}, "queued job failed")

	var initialCount int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM work_items WHERE job_id = ?`, job.JobID).Scan(&initialCount); err != nil {
		t.Fatalf("count initial work items: %v", err)
	}
	if initialCount == 0 {
		t.Fatalf("expected at least one queued work item")
	}

	for i := 0; i < 4; i++ {
		app.processJobQueueOnce()
		app.processWorkQueueOnce()
		time.Sleep(40 * time.Millisecond)
	}

	var afterCount int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM work_items WHERE job_id = ?`, job.JobID).Scan(&afterCount); err != nil {
		t.Fatalf("count work items after reruns: %v", err)
	}
	if afterCount != initialCount {
		t.Fatalf("expected failed job to stop requeueing work items (before=%d after=%d)", initialCount, afterCount)
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
