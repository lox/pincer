package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	protocolv1 "github.com/lox/pincer/gen/proto/pincer/protocol/v1"
	"github.com/lox/pincer/gen/proto/pincer/protocol/v1/protocolv1connect"
	"github.com/lox/pincer/internal/agent"
)

func TestStartTurnStreamsLifecycleEvents(t *testing.T) {
	t.Parallel()

	app := newTestAppWithPlanner(t, stubPlanner{
		result: agent.PlanResult{
			AssistantMessage: "Proposing command execution.",
			ProposedActions: []agent.ProposedAction{
				{
					Tool:          "run_bash",
					Args:          []byte(`{"command":"echo hello"}`),
					Justification: "User requested shell command.",
					RiskClass:     "READ",
				},
			},
		},
	})
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapConnectToken(t, srv.URL)
	httpClient := newAuthorizedHTTPClient(token)

	threadsClient := protocolv1connect.NewThreadsServiceClient(httpClient, srv.URL)
	turnsClient := protocolv1connect.NewTurnsServiceClient(httpClient, srv.URL)

	createResp, err := threadsClient.CreateThread(context.Background(), connect.NewRequest(&protocolv1.CreateThreadRequest{}))
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	threadID := createResp.Msg.GetThreadId()

	stream, err := turnsClient.StartTurn(context.Background(), connect.NewRequest(&protocolv1.StartTurnRequest{
		ThreadId:    threadID,
		UserText:    "run pwd",
		TriggerType: protocolv1.TriggerType_CHAT_MESSAGE,
	}))
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}

	gotStarted := false
	gotProposal := false
	gotCompleted := false

	for stream.Receive() {
		event := stream.Msg()
		if event.GetTurnStarted() != nil {
			gotStarted = true
		}
		if event.GetProposedActionCreated() != nil {
			gotProposal = true
		}
		if event.GetTurnCompleted() != nil {
			gotCompleted = true
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("start turn stream err: %v", err)
	}

	if !gotStarted {
		t.Fatalf("expected TurnStarted event")
	}
	if !gotProposal {
		t.Fatalf("expected ProposedActionCreated event")
	}
	if !gotCompleted {
		t.Fatalf("expected TurnCompleted event")
	}
}

func TestSendTurnReturnsProposedActionID(t *testing.T) {
	t.Parallel()

	app := newTestAppWithPlanner(t, stubPlanner{
		result: agent.PlanResult{
			AssistantMessage: "Proposing command execution.",
			ProposedActions: []agent.ProposedAction{
				{
					Tool:          "run_bash",
					Args:          []byte(`{"command":"pwd"}`),
					Justification: "User requested shell command.",
					RiskClass:     "READ",
				},
			},
		},
	})
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapConnectToken(t, srv.URL)
	httpClient := newAuthorizedHTTPClient(token)

	threadsClient := protocolv1connect.NewThreadsServiceClient(httpClient, srv.URL)
	turnsClient := protocolv1connect.NewTurnsServiceClient(httpClient, srv.URL)
	approvalsClient := protocolv1connect.NewApprovalsServiceClient(httpClient, srv.URL)

	createResp, err := threadsClient.CreateThread(context.Background(), connect.NewRequest(&protocolv1.CreateThreadRequest{}))
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}

	sendResp, err := turnsClient.SendTurn(context.Background(), connect.NewRequest(&protocolv1.SendTurnRequest{
		ThreadId:    createResp.Msg.GetThreadId(),
		UserText:    "run pwd",
		TriggerType: protocolv1.TriggerType_CHAT_MESSAGE,
	}))
	if err != nil {
		t.Fatalf("send turn: %v", err)
	}

	if sendResp.Msg.GetTurnId() == "" {
		t.Fatalf("expected non-empty turn_id")
	}
	if sendResp.Msg.GetActionId() == "" {
		t.Fatalf("expected non-empty action_id")
	}
	if sendResp.Msg.GetAssistantMessage() != "" {
		t.Fatalf("expected empty assistant_message when proposal is emitted, got %q", sendResp.Msg.GetAssistantMessage())
	}

	pendingResp, err := approvalsClient.ListApprovals(context.Background(), connect.NewRequest(&protocolv1.ListApprovalsRequest{
		Status: protocolv1.ActionStatus_PENDING,
	}))
	if err != nil {
		t.Fatalf("list approvals: %v", err)
	}
	if len(pendingResp.Msg.GetItems()) != 1 {
		t.Fatalf("expected 1 pending approval, got %d", len(pendingResp.Msg.GetItems()))
	}
	if pendingResp.Msg.GetItems()[0].GetActionId() != sendResp.Msg.GetActionId() {
		t.Fatalf("expected send turn action_id %s to match pending action %s", sendResp.Msg.GetActionId(), pendingResp.Msg.GetItems()[0].GetActionId())
	}
}

func TestWatchThreadStreamsBashOutputAndExecutionStatus(t *testing.T) {
	t.Parallel()

	app := newTestAppWithPlanner(t, stubPlanner{
		result: agent.PlanResult{
			AssistantMessage: "Proposing command execution.",
			ProposedActions: []agent.ProposedAction{
				{
					Tool:          "run_bash",
					Args:          []byte(`{"command":"printf 'stdout-line\\n'; printf 'stderr-line\\n' 1>&2"}`),
					Justification: "User requested shell command.",
					RiskClass:     "READ",
				},
			},
		},
	})
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapConnectToken(t, srv.URL)
	httpClient := newAuthorizedHTTPClient(token)

	threadsClient := protocolv1connect.NewThreadsServiceClient(httpClient, srv.URL)
	turnsClient := protocolv1connect.NewTurnsServiceClient(httpClient, srv.URL)
	eventsClient := protocolv1connect.NewEventsServiceClient(httpClient, srv.URL)
	approvalsClient := protocolv1connect.NewApprovalsServiceClient(httpClient, srv.URL)

	createResp, err := threadsClient.CreateThread(context.Background(), connect.NewRequest(&protocolv1.CreateThreadRequest{}))
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	threadID := createResp.Msg.GetThreadId()

	turnStream, err := turnsClient.StartTurn(context.Background(), connect.NewRequest(&protocolv1.StartTurnRequest{
		ThreadId:    threadID,
		UserText:    "run shell command",
		TriggerType: protocolv1.TriggerType_CHAT_MESSAGE,
	}))
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	for turnStream.Receive() {
		// Drain until turn completes.
	}
	if err := turnStream.Err(); err != nil {
		t.Fatalf("turn stream err: %v", err)
	}

	pendingResp, err := approvalsClient.ListApprovals(context.Background(), connect.NewRequest(&protocolv1.ListApprovalsRequest{
		Status: protocolv1.ActionStatus_PENDING,
	}))
	if err != nil {
		t.Fatalf("list approvals: %v", err)
	}
	if len(pendingResp.Msg.GetItems()) != 1 {
		t.Fatalf("expected 1 pending approval, got %d", len(pendingResp.Msg.GetItems()))
	}
	actionID := pendingResp.Msg.GetItems()[0].GetActionId()

	watchCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	watchStream, err := eventsClient.WatchThread(watchCtx, connect.NewRequest(&protocolv1.WatchThreadRequest{
		ThreadId:     threadID,
		FromSequence: 0,
	}))
	if err != nil {
		t.Fatalf("watch thread: %v", err)
	}

	if _, err := approvalsClient.ApproveAction(context.Background(), connect.NewRequest(&protocolv1.ApproveActionRequest{ActionId: actionID})); err != nil {
		t.Fatalf("approve action: %v", err)
	}

	gotOutput := false
	gotFinished := false
	gotExecutedStatus := false

	for watchStream.Receive() {
		event := watchStream.Msg()
		if delta := event.GetToolExecutionOutputDelta(); delta != nil && len(delta.GetChunk()) > 0 {
			gotOutput = true
		}
		if event.GetToolExecutionFinished() != nil {
			gotFinished = true
		}
		if statusChanged := event.GetProposedActionStatusChanged(); statusChanged != nil && statusChanged.GetActionId() == actionID && statusChanged.GetStatus() == protocolv1.ActionStatus_EXECUTED {
			gotExecutedStatus = true
		}
		if gotOutput && gotFinished && gotExecutedStatus {
			_ = watchStream.Close()
			break
		}
	}

	if err := watchStream.Err(); err != nil && watchCtx.Err() == nil {
		t.Fatalf("watch thread stream err: %v", err)
	}
	if !gotOutput {
		t.Fatalf("expected streamed command output chunks")
	}
	if !gotFinished {
		t.Fatalf("expected ToolExecutionFinished event")
	}
	if !gotExecutedStatus {
		t.Fatalf("expected EXECUTED ProposedActionStatusChanged event")
	}
}

func bootstrapConnectToken(t *testing.T, baseURL string) string {
	t.Helper()

	authClient := protocolv1connect.NewAuthServiceClient(http.DefaultClient, baseURL)
	codeResp, err := authClient.CreatePairingCode(context.Background(), connect.NewRequest(&protocolv1.CreatePairingCodeRequest{}))
	if err != nil {
		t.Fatalf("create pairing code: %v", err)
	}
	bindResp, err := authClient.BindPairingCode(context.Background(), connect.NewRequest(&protocolv1.BindPairingCodeRequest{
		Code:       codeResp.Msg.GetCode(),
		DeviceName: "test-device",
	}))
	if err != nil {
		t.Fatalf("bind pairing code: %v", err)
	}
	return bindResp.Msg.GetToken()
}

func newAuthorizedHTTPClient(token string) *http.Client {
	return &http.Client{
		Transport: &authTransport{token: token, base: http.DefaultTransport},
	}
}

type authTransport struct {
	token string
	base  http.RoundTripper
}

func (a *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := a.base
	if base == nil {
		base = http.DefaultTransport
	}
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+a.token)
	return base.RoundTrip(clone)
}
