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
					Args:          []byte(`{"command":"deploy hello"}`),
					Justification: "User requested shell command.",
					RiskClass:     "HIGH",
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
	gotPaused := false
	gotAssistantText := false

	for stream.Receive() {
		event := stream.Msg()
		if event.GetTurnStarted() != nil {
			gotStarted = true
		}
		if event.GetProposedActionCreated() != nil {
			gotProposal = true
		}
		if event.GetTurnPaused() != nil {
			gotPaused = true
		}
		if event.GetAssistantTextDelta() != nil {
			gotAssistantText = true
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
	if !gotPaused {
		t.Fatalf("expected TurnPaused event (turn pauses when proposals require approval)")
	}
	if !gotAssistantText {
		t.Fatalf("expected AssistantTextDelta event (assistant text is always emitted)")
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
					Args:          []byte(`{"command":"deploy"}`),
					Justification: "User requested shell command.",
					RiskClass:     "HIGH",
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
	if sendResp.Msg.GetAssistantMessage() != "Proposing command execution." {
		t.Fatalf("expected assistant_message to be persisted with proposal, got %q", sendResp.Msg.GetAssistantMessage())
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
	approval := pendingResp.Msg.GetItems()[0]
	if approval.GetActionId() != sendResp.Msg.GetActionId() {
		t.Fatalf("expected send turn action_id %s to match pending action %s", sendResp.Msg.GetActionId(), approval.GetActionId())
	}
	if approval.GetDeterministicSummary() == "" {
		t.Fatalf("expected deterministic summary in approval response")
	}
	if approval.GetPreview() == nil {
		t.Fatalf("expected preview payload in approval response")
	}
	if command := approval.GetPreview().GetFields()["command"].GetStringValue(); command != "deploy" {
		t.Fatalf("expected preview.command to be %q, got %q", "deploy", command)
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
					Args:          []byte(`{"command":"deploy --verbose"}`),
					Justification: "User requested shell command.",
					RiskClass:     "HIGH",
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

func TestListThreadsReturnsThreadsOrderedByUpdatedAt(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapConnectToken(t, srv.URL)
	httpClient := newAuthorizedHTTPClient(token)

	threadsClient := protocolv1connect.NewThreadsServiceClient(httpClient, srv.URL)
	turnsClient := protocolv1connect.NewTurnsServiceClient(httpClient, srv.URL)

	// Create two threads.
	create1, err := threadsClient.CreateThread(context.Background(), connect.NewRequest(&protocolv1.CreateThreadRequest{}))
	if err != nil {
		t.Fatalf("create thread 1: %v", err)
	}
	thread1 := create1.Msg.GetThreadId()

	create2, err := threadsClient.CreateThread(context.Background(), connect.NewRequest(&protocolv1.CreateThreadRequest{}))
	if err != nil {
		t.Fatalf("create thread 2: %v", err)
	}
	thread2 := create2.Msg.GetThreadId()

	// Send a message to thread1 to give it a title and update its updated_at.
	_, err = turnsClient.SendTurn(context.Background(), connect.NewRequest(&protocolv1.SendTurnRequest{
		ThreadId:    thread1,
		UserText:    "Hello from thread one",
		TriggerType: protocolv1.TriggerType_CHAT_MESSAGE,
	}))
	if err != nil {
		t.Fatalf("send turn to thread1: %v", err)
	}

	// List threads — thread1 should come first (most recently updated).
	listResp, err := threadsClient.ListThreads(context.Background(), connect.NewRequest(&protocolv1.ListThreadsRequest{}))
	if err != nil {
		t.Fatalf("list threads: %v", err)
	}

	items := listResp.Msg.GetItems()
	if len(items) < 2 {
		t.Fatalf("expected at least 2 threads, got %d", len(items))
	}

	// First item should be thread1 (most recently updated via message).
	if items[0].GetThreadId() != thread1 {
		t.Fatalf("expected first thread to be %s (most recently updated), got %s", thread1, items[0].GetThreadId())
	}

	// Thread1 should have a title derived from the user message.
	if items[0].GetTitle() == "" {
		t.Fatalf("expected thread1 to have a title from the first user message")
	}
	if items[0].GetTitle() != "Hello from thread one" {
		t.Fatalf("expected title %q, got %q", "Hello from thread one", items[0].GetTitle())
	}

	// Thread1 should have a message count > 0.
	if items[0].GetMessageCount() == 0 {
		t.Fatalf("expected thread1 to have message_count > 0")
	}

	// Thread2 should have no title (no messages sent).
	found := false
	for _, item := range items {
		if item.GetThreadId() == thread2 {
			found = true
			if item.GetTitle() != "" {
				t.Fatalf("expected thread2 to have empty title, got %q", item.GetTitle())
			}
		}
	}
	if !found {
		t.Fatalf("expected to find thread2 in list")
	}
}

func TestDeleteThreadRemovesThreadAndMessages(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapConnectToken(t, srv.URL)
	httpClient := newAuthorizedHTTPClient(token)

	threadsClient := protocolv1connect.NewThreadsServiceClient(httpClient, srv.URL)
	turnsClient := protocolv1connect.NewTurnsServiceClient(httpClient, srv.URL)

	// Create a thread and send a message.
	createResp, err := threadsClient.CreateThread(context.Background(), connect.NewRequest(&protocolv1.CreateThreadRequest{}))
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	threadID := createResp.Msg.GetThreadId()

	_, err = turnsClient.SendTurn(context.Background(), connect.NewRequest(&protocolv1.SendTurnRequest{
		ThreadId:    threadID,
		UserText:    "hello",
		TriggerType: protocolv1.TriggerType_CHAT_MESSAGE,
	}))
	if err != nil {
		t.Fatalf("send turn: %v", err)
	}

	// Verify it appears in ListThreads.
	listResp, err := threadsClient.ListThreads(context.Background(), connect.NewRequest(&protocolv1.ListThreadsRequest{}))
	if err != nil {
		t.Fatalf("list threads: %v", err)
	}
	foundBefore := false
	for _, item := range listResp.Msg.GetItems() {
		if item.GetThreadId() == threadID {
			foundBefore = true
		}
	}
	if !foundBefore {
		t.Fatalf("expected thread to appear in list before deletion")
	}

	// Delete the thread.
	_, err = threadsClient.DeleteThread(context.Background(), connect.NewRequest(&protocolv1.DeleteThreadRequest{
		ThreadId: threadID,
	}))
	if err != nil {
		t.Fatalf("delete thread: %v", err)
	}

	// Thread should no longer appear in ListThreads.
	listResp2, err := threadsClient.ListThreads(context.Background(), connect.NewRequest(&protocolv1.ListThreadsRequest{}))
	if err != nil {
		t.Fatalf("list threads after delete: %v", err)
	}
	for _, item := range listResp2.Msg.GetItems() {
		if item.GetThreadId() == threadID {
			t.Fatalf("expected thread to be gone after deletion, but found it in list")
		}
	}

	// GetThreadSnapshot should return NOT_FOUND.
	_, err = threadsClient.GetThreadSnapshot(context.Background(), connect.NewRequest(&protocolv1.GetThreadSnapshotRequest{
		ThreadId: threadID,
	}))
	if err == nil {
		t.Fatalf("expected error when getting deleted thread snapshot")
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expected NOT_FOUND error, got %v", err)
	}
}

func TestDeleteThreadNotFoundReturnsError(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapConnectToken(t, srv.URL)
	httpClient := newAuthorizedHTTPClient(token)

	threadsClient := protocolv1connect.NewThreadsServiceClient(httpClient, srv.URL)

	_, err := threadsClient.DeleteThread(context.Background(), connect.NewRequest(&protocolv1.DeleteThreadRequest{
		ThreadId: "thr_nonexistent",
	}))
	if err == nil {
		t.Fatalf("expected error when deleting nonexistent thread")
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expected NOT_FOUND error, got %v", err)
	}
}

func TestThreadTitleTruncatesLongMessages(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
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

	// Send a message longer than 80 characters.
	longMessage := "This is a very long message that should be truncated when used as a thread title because it exceeds the maximum allowed length for titles"
	_, err = turnsClient.SendTurn(context.Background(), connect.NewRequest(&protocolv1.SendTurnRequest{
		ThreadId:    threadID,
		UserText:    longMessage,
		TriggerType: protocolv1.TriggerType_CHAT_MESSAGE,
	}))
	if err != nil {
		t.Fatalf("send turn: %v", err)
	}

	listResp, err := threadsClient.ListThreads(context.Background(), connect.NewRequest(&protocolv1.ListThreadsRequest{}))
	if err != nil {
		t.Fatalf("list threads: %v", err)
	}

	for _, item := range listResp.Msg.GetItems() {
		if item.GetThreadId() == threadID {
			title := item.GetTitle()
			if len(title) > 84 { // 80 + room for "…" (3 bytes UTF-8)
				t.Fatalf("expected title to be truncated, got %d chars: %q", len(title), title)
			}
			if title == longMessage {
				t.Fatalf("expected title to be truncated, got full message")
			}
			return
		}
	}
	t.Fatalf("expected to find thread in list")
}

func TestThreadTitleNotOverwrittenBySecondMessage(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
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

	// Send first message.
	_, err = turnsClient.SendTurn(context.Background(), connect.NewRequest(&protocolv1.SendTurnRequest{
		ThreadId:    threadID,
		UserText:    "first message title",
		TriggerType: protocolv1.TriggerType_CHAT_MESSAGE,
	}))
	if err != nil {
		t.Fatalf("send turn 1: %v", err)
	}

	// Send second message.
	_, err = turnsClient.SendTurn(context.Background(), connect.NewRequest(&protocolv1.SendTurnRequest{
		ThreadId:    threadID,
		UserText:    "second message should not overwrite",
		TriggerType: protocolv1.TriggerType_CHAT_MESSAGE,
	}))
	if err != nil {
		t.Fatalf("send turn 2: %v", err)
	}

	listResp, err := threadsClient.ListThreads(context.Background(), connect.NewRequest(&protocolv1.ListThreadsRequest{}))
	if err != nil {
		t.Fatalf("list threads: %v", err)
	}

	for _, item := range listResp.Msg.GetItems() {
		if item.GetThreadId() == threadID {
			if item.GetTitle() != "first message title" {
				t.Fatalf("expected title to remain %q, got %q", "first message title", item.GetTitle())
			}
			return
		}
	}
	t.Fatalf("expected to find thread in list")
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
