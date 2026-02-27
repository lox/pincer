package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	protocolv1 "github.com/lox/pincer/gen/proto/pincer/protocol/v1"
	"github.com/lox/pincer/gen/proto/pincer/protocol/v1/protocolv1connect"
	"github.com/lox/pincer/internal/agent"
	"google.golang.org/protobuf/types/known/structpb"
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

func TestListThreadsExcludesSystemChannelThreads(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapConnectToken(t, srv.URL)
	httpClient := newAuthorizedHTTPClient(token)

	threadsClient := protocolv1connect.NewThreadsServiceClient(httpClient, srv.URL)

	createResp, err := threadsClient.CreateThread(context.Background(), connect.NewRequest(&protocolv1.CreateThreadRequest{}))
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	iosThreadID := createResp.Msg.GetThreadId()

	systemThreadID := "thread_heartbeat"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := app.db.Exec(`
		INSERT INTO threads(thread_id, user_id, channel, created_at, title, updated_at)
		VALUES(?, ?, 'system', ?, 'Heartbeat', ?)
	`, systemThreadID, app.ownerID, now, now); err != nil {
		t.Fatalf("insert system thread: %v", err)
	}

	listResp, err := threadsClient.ListThreads(context.Background(), connect.NewRequest(&protocolv1.ListThreadsRequest{}))
	if err != nil {
		t.Fatalf("list threads: %v", err)
	}

	var sawIOSThread bool
	for _, item := range listResp.Msg.GetItems() {
		if item.GetThreadId() == systemThreadID {
			t.Fatalf("expected system thread %q to be excluded from ListThreads", systemThreadID)
		}
		if item.GetThreadId() == iosThreadID {
			sawIOSThread = true
		}
	}
	if !sawIOSThread {
		t.Fatalf("expected ios thread %q to be present in ListThreads", iosThreadID)
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

func TestSpawnToolCreatesBackgroundJobAndPostsCompletionSummary(t *testing.T) {
	t.Parallel()

	planner := &staticPlannerFunc{fn: func(_ context.Context, req agent.PlanRequest) (agent.PlanResult, error) {
		switch req.UserMessage {
		case "start background research":
			for _, msg := range req.History {
				if strings.Contains(msg.Content, "[tool_result:spawn]") {
					return agent.PlanResult{AssistantMessage: "Background job started."}, nil
				}
			}
			return agent.PlanResult{
				AssistantMessage: "Starting background work.",
				ProposedActions: []agent.ProposedAction{{
					Tool:          "spawn",
					Args:          []byte(`{"goal":"collect release notes","max_tool_steps":4}`),
					Justification: "Run the longer task in background.",
					RiskClass:     "READ",
				}},
			}, nil
		case "collect release notes":
			return agent.PlanResult{AssistantMessage: "Background summary: all release notes collected."}, nil
		default:
			return agent.PlanResult{AssistantMessage: "ok"}, nil
		}
	}}

	app := newTestAppWithPlanner(t, planner)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapConnectToken(t, srv.URL)
	httpClient := newAuthorizedHTTPClient(token)

	threadsClient := protocolv1connect.NewThreadsServiceClient(httpClient, srv.URL)
	turnsClient := protocolv1connect.NewTurnsServiceClient(httpClient, srv.URL)
	jobsClient := protocolv1connect.NewJobsServiceClient(httpClient, srv.URL)

	createResp, err := threadsClient.CreateThread(context.Background(), connect.NewRequest(&protocolv1.CreateThreadRequest{}))
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	threadID := createResp.Msg.GetThreadId()

	if _, err := turnsClient.SendTurn(context.Background(), connect.NewRequest(&protocolv1.SendTurnRequest{
		ThreadId:    threadID,
		UserText:    "start background research",
		TriggerType: protocolv1.TriggerType_CHAT_MESSAGE,
	})); err != nil {
		t.Fatalf("send turn: %v", err)
	}

	waitForCondition(t, 5*time.Second, func() bool {
		jobsResp, err := jobsClient.ListJobs(context.Background(), connect.NewRequest(&protocolv1.ListJobsRequest{}))
		if err != nil || len(jobsResp.Msg.GetItems()) == 0 {
			return false
		}
		return jobsResp.Msg.GetItems()[0].GetStatus() == protocolv1.JobStatus_JOB_COMPLETED
	}, "spawned job completion")

	messagesResp, err := threadsClient.ListThreadMessages(context.Background(), connect.NewRequest(&protocolv1.ListThreadMessagesRequest{ThreadId: threadID}))
	if err != nil {
		t.Fatalf("list thread messages: %v", err)
	}

	foundSummary := false
	for _, item := range messagesResp.Msg.GetItems() {
		if item.GetRole() == "system" && strings.Contains(item.GetContent(), "Background job") && strings.Contains(item.GetContent(), "release notes") {
			foundSummary = true
			break
		}
	}
	if !foundSummary {
		t.Fatalf("expected background job completion summary in originating thread")
	}
}

func TestJobsListToolExecutesInlineWithoutApproval(t *testing.T) {
	t.Parallel()

	planner := &staticPlannerFunc{fn: func(_ context.Context, req agent.PlanRequest) (agent.PlanResult, error) {
		switch req.UserMessage {
		case "check active jobs":
			for _, msg := range req.History {
				if strings.Contains(msg.Content, "[tool_result:jobs_list]") {
					return agent.PlanResult{AssistantMessage: "I found current background job state from the DB."}, nil
				}
			}
			return agent.PlanResult{
				AssistantMessage: "Checking current background jobs.",
				ProposedActions: []agent.ProposedAction{{
					Tool:          "jobs_list",
					Args:          []byte(`{}`),
					Justification: "Check internal background job state.",
					RiskClass:     "READ",
				}},
			}, nil
		default:
			return agent.PlanResult{AssistantMessage: "ok"}, nil
		}
	}}

	app := newTestAppWithPlanner(t, planner)
	if _, err := app.createJob(context.Background(), createJobInput{
		Goal:          "seeded job",
		TriggerType:   protocolv1.TriggerType_JOB_WAKEUP,
		TriggerSource: "seed",
	}); err != nil {
		t.Fatalf("seed job: %v", err)
	}

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
	threadID := createResp.Msg.GetThreadId()

	if _, err := turnsClient.SendTurn(context.Background(), connect.NewRequest(&protocolv1.SendTurnRequest{
		ThreadId:    threadID,
		UserText:    "check active jobs",
		TriggerType: protocolv1.TriggerType_CHAT_MESSAGE,
	})); err != nil {
		t.Fatalf("send turn: %v", err)
	}

	pendingResp, err := approvalsClient.ListApprovals(context.Background(), connect.NewRequest(&protocolv1.ListApprovalsRequest{Status: protocolv1.ActionStatus_PENDING}))
	if err != nil {
		t.Fatalf("list pending approvals: %v", err)
	}
	if len(pendingResp.Msg.GetItems()) != 0 {
		t.Fatalf("expected jobs_list to execute inline without approvals, got %d pending", len(pendingResp.Msg.GetItems()))
	}

	messagesResp, err := threadsClient.ListThreadMessages(context.Background(), connect.NewRequest(&protocolv1.ListThreadMessagesRequest{ThreadId: threadID}))
	if err != nil {
		t.Fatalf("list thread messages: %v", err)
	}

	foundAssistant := false
	for _, item := range messagesResp.Msg.GetItems() {
		if item.GetRole() == "assistant" && strings.Contains(item.GetContent(), "background job state") {
			foundAssistant = true
			break
		}
	}
	if !foundAssistant {
		t.Fatalf("expected assistant response after jobs_list inline execution")
	}
}

func TestJobsServiceCreateListGetAndCancel(t *testing.T) {
	t.Parallel()

	planner := &staticPlannerFunc{fn: func(_ context.Context, req agent.PlanRequest) (agent.PlanResult, error) {
		if req.UserMessage == "job requires approval" {
			return agent.PlanResult{
				AssistantMessage: "Need approval for command.",
				ProposedActions: []agent.ProposedAction{{
					Tool:          "run_bash",
					Args:          []byte(`{"command":"echo hello"}`),
					Justification: "Needs shell command.",
					RiskClass:     "HIGH",
				}},
			}, nil
		}
		return agent.PlanResult{AssistantMessage: "ok"}, nil
	}}

	app := newTestAppWithPlanner(t, planner)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapConnectToken(t, srv.URL)
	httpClient := newAuthorizedHTTPClient(token)

	jobsClient := protocolv1connect.NewJobsServiceClient(httpClient, srv.URL)
	approvalsClient := protocolv1connect.NewApprovalsServiceClient(httpClient, srv.URL)

	createResp, err := jobsClient.CreateJob(context.Background(), connect.NewRequest(&protocolv1.CreateJobRequest{Goal: "job requires approval"}))
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	jobID := createResp.Msg.GetItem().GetJobId()
	if jobID == "" {
		t.Fatalf("expected create job response to include job_id")
	}

	waitForCondition(t, 5*time.Second, func() bool {
		resp, err := jobsClient.GetJob(context.Background(), connect.NewRequest(&protocolv1.GetJobRequest{JobId: jobID}))
		if err != nil {
			return false
		}
		return resp.Msg.GetItem().GetStatus() == protocolv1.JobStatus_JOB_WAITING_APPROVAL
	}, "job waiting approval")

	pendingResp, err := approvalsClient.ListApprovals(context.Background(), connect.NewRequest(&protocolv1.ListApprovalsRequest{Status: protocolv1.ActionStatus_PENDING}))
	if err != nil {
		t.Fatalf("list pending approvals: %v", err)
	}
	if len(pendingResp.Msg.GetItems()) == 0 {
		t.Fatalf("expected pending approval for waiting job")
	}

	if _, err := jobsClient.ListJobs(context.Background(), connect.NewRequest(&protocolv1.ListJobsRequest{})); err != nil {
		t.Fatalf("list jobs: %v", err)
	}

	cancelResp, err := jobsClient.CancelJob(context.Background(), connect.NewRequest(&protocolv1.CancelJobRequest{JobId: jobID}))
	if err != nil {
		t.Fatalf("cancel job: %v", err)
	}
	if cancelResp.Msg.GetStatus() != protocolv1.JobStatus_JOB_CANCELLED {
		t.Fatalf("expected cancel status %v, got %v", protocolv1.JobStatus_JOB_CANCELLED, cancelResp.Msg.GetStatus())
	}

	jobResp, err := jobsClient.GetJob(context.Background(), connect.NewRequest(&protocolv1.GetJobRequest{JobId: jobID}))
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if jobResp.Msg.GetItem().GetStatus() != protocolv1.JobStatus_JOB_CANCELLED {
		t.Fatalf("expected cancelled job, got %v", jobResp.Msg.GetItem().GetStatus())
	}

	pendingAfterCancel, err := approvalsClient.ListApprovals(context.Background(), connect.NewRequest(&protocolv1.ListApprovalsRequest{Status: protocolv1.ActionStatus_PENDING}))
	if err != nil {
		t.Fatalf("list approvals after cancel: %v", err)
	}
	if len(pendingAfterCancel.Msg.GetItems()) != 0 {
		t.Fatalf("expected no pending approvals after job cancellation, got %d", len(pendingAfterCancel.Msg.GetItems()))
	}
}

func TestJobProposalsResumeAfterApprovalAndComplete(t *testing.T) {
	t.Parallel()

	planner := &staticPlannerFunc{fn: func(_ context.Context, req agent.PlanRequest) (agent.PlanResult, error) {
		switch req.UserMessage {
		case "request background action":
			for _, msg := range req.History {
				if strings.Contains(msg.Content, "[tool_result:spawn]") {
					return agent.PlanResult{AssistantMessage: "Spawned background job."}, nil
				}
			}
			return agent.PlanResult{
				AssistantMessage: "Spawning a background job.",
				ProposedActions: []agent.ProposedAction{{
					Tool:          "spawn",
					Args:          []byte(`{"goal":"job needs approval","max_tool_steps":6}`),
					Justification: "Run asynchronously.",
					RiskClass:     "READ",
				}},
			}, nil
		case "job needs approval":
			for _, msg := range req.History {
				if strings.Contains(msg.Content, "[tool_result:run_bash]") {
					return agent.PlanResult{AssistantMessage: "Job finished after approval."}, nil
				}
			}
			return agent.PlanResult{
				AssistantMessage: "Need approval from job.",
				ProposedActions: []agent.ProposedAction{{
					Tool:          "run_bash",
					Args:          []byte(`{"command":"echo from job"}`),
					Justification: "Needs shell command.",
					RiskClass:     "HIGH",
				}},
			}, nil
		default:
			return agent.PlanResult{AssistantMessage: "ok"}, nil
		}
	}}

	app := newTestAppWithPlanner(t, planner)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapConnectToken(t, srv.URL)
	httpClient := newAuthorizedHTTPClient(token)

	threadsClient := protocolv1connect.NewThreadsServiceClient(httpClient, srv.URL)
	turnsClient := protocolv1connect.NewTurnsServiceClient(httpClient, srv.URL)
	jobsClient := protocolv1connect.NewJobsServiceClient(httpClient, srv.URL)
	approvalsClient := protocolv1connect.NewApprovalsServiceClient(httpClient, srv.URL)

	createResp, err := threadsClient.CreateThread(context.Background(), connect.NewRequest(&protocolv1.CreateThreadRequest{}))
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	originThreadID := createResp.Msg.GetThreadId()

	if _, err := turnsClient.SendTurn(context.Background(), connect.NewRequest(&protocolv1.SendTurnRequest{
		ThreadId:    originThreadID,
		UserText:    "request background action",
		TriggerType: protocolv1.TriggerType_CHAT_MESSAGE,
	})); err != nil {
		t.Fatalf("send turn: %v", err)
	}

	var jobID string
	waitForCondition(t, 5*time.Second, func() bool {
		jobsResp, err := jobsClient.ListJobs(context.Background(), connect.NewRequest(&protocolv1.ListJobsRequest{}))
		if err != nil || len(jobsResp.Msg.GetItems()) == 0 {
			return false
		}
		jobID = jobsResp.Msg.GetItems()[0].GetJobId()
		return jobsResp.Msg.GetItems()[0].GetStatus() == protocolv1.JobStatus_JOB_WAITING_APPROVAL
	}, "job waiting approval")

	pendingResp, err := approvalsClient.ListApprovals(context.Background(), connect.NewRequest(&protocolv1.ListApprovalsRequest{Status: protocolv1.ActionStatus_PENDING}))
	if err != nil {
		t.Fatalf("list pending approvals: %v", err)
	}
	if len(pendingResp.Msg.GetItems()) == 0 {
		t.Fatalf("expected pending approval from job turn")
	}
	actionID := pendingResp.Msg.GetItems()[0].GetActionId()

	if _, err := approvalsClient.ApproveAction(context.Background(), connect.NewRequest(&protocolv1.ApproveActionRequest{ActionId: actionID})); err != nil {
		t.Fatalf("approve action: %v", err)
	}

	waitForCondition(t, 8*time.Second, func() bool {
		jobResp, err := jobsClient.GetJob(context.Background(), connect.NewRequest(&protocolv1.GetJobRequest{JobId: jobID}))
		if err != nil {
			return false
		}
		return jobResp.Msg.GetItem().GetStatus() == protocolv1.JobStatus_JOB_COMPLETED
	}, "job completion after approval")

	messagesResp, err := threadsClient.ListThreadMessages(context.Background(), connect.NewRequest(&protocolv1.ListThreadMessagesRequest{ThreadId: originThreadID}))
	if err != nil {
		t.Fatalf("list origin thread messages: %v", err)
	}

	foundCompletion := false
	for _, item := range messagesResp.Msg.GetItems() {
		if item.GetRole() == "system" && strings.Contains(item.GetContent(), "Job finished after approval") {
			foundCompletion = true
			break
		}
	}
	if !foundCompletion {
		t.Fatalf("expected job completion summary in originating thread")
	}
}

func TestScheduleRunNowDedupesWhenJobAlreadyActive(t *testing.T) {
	t.Parallel()

	planner := &staticPlannerFunc{fn: func(_ context.Context, req agent.PlanRequest) (agent.PlanResult, error) {
		if req.UserMessage == "schedule goal needs approval" {
			return agent.PlanResult{
				AssistantMessage: "Need approval for scheduled command.",
				ProposedActions: []agent.ProposedAction{{
					Tool:          "run_bash",
					Args:          []byte(`{"command":"echo from schedule"}`),
					Justification: "Scheduled work needs shell command.",
					RiskClass:     "HIGH",
				}},
			}, nil
		}
		return agent.PlanResult{AssistantMessage: "ok"}, nil
	}}

	app := newTestAppWithPlanner(t, planner)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapConnectToken(t, srv.URL)
	httpClient := newAuthorizedHTTPClient(token)

	schedulesClient := protocolv1connect.NewSchedulesServiceClient(httpClient, srv.URL)
	jobsClient := protocolv1connect.NewJobsServiceClient(httpClient, srv.URL)

	createResp, err := schedulesClient.CreateSchedule(context.Background(), connect.NewRequest(&protocolv1.CreateScheduleRequest{
		Name:        "schedule goal needs approval",
		TriggerKind: protocolv1.ScheduleTriggerKind_SCHEDULE_TRIGGER_INTERVAL,
		TriggerSpec: "15m",
		Timezone:    "UTC",
	}))
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	scheduleID := createResp.Msg.GetItem().GetScheduleId()

	firstRun, err := schedulesClient.RunScheduleNow(context.Background(), connect.NewRequest(&protocolv1.RunScheduleNowRequest{ScheduleId: scheduleID}))
	if err != nil {
		t.Fatalf("run schedule now (first): %v", err)
	}
	if firstRun.Msg.GetWakeupEventId() == "" {
		t.Fatalf("expected wakeup_event_id from first run")
	}
	if firstRun.Msg.GetJobId() == "" {
		t.Fatalf("expected first run to dispatch a job")
	}

	waitForCondition(t, 5*time.Second, func() bool {
		jobsResp, err := jobsClient.ListJobs(context.Background(), connect.NewRequest(&protocolv1.ListJobsRequest{}))
		if err != nil {
			return false
		}
		count := 0
		waitingApproval := false
		for _, item := range jobsResp.Msg.GetItems() {
			if item.GetTriggerSourceId() != scheduleID {
				continue
			}
			count++
			if item.GetStatus() == protocolv1.JobStatus_JOB_WAITING_APPROVAL {
				waitingApproval = true
			}
		}
		return count == 1 && waitingApproval
	}, "first schedule run waiting approval")

	secondRun, err := schedulesClient.RunScheduleNow(context.Background(), connect.NewRequest(&protocolv1.RunScheduleNowRequest{ScheduleId: scheduleID}))
	if err != nil {
		t.Fatalf("run schedule now (second): %v", err)
	}
	if secondRun.Msg.GetWakeupEventId() == "" {
		t.Fatalf("expected wakeup_event_id from second run")
	}
	if secondRun.Msg.GetJobId() != "" {
		t.Fatalf("expected second run to be deduped while first job is active")
	}

	waitForCondition(t, 3*time.Second, func() bool {
		jobsResp, err := jobsClient.ListJobs(context.Background(), connect.NewRequest(&protocolv1.ListJobsRequest{}))
		if err != nil {
			return false
		}
		count := 0
		for _, item := range jobsResp.Msg.GetItems() {
			if item.GetTriggerSourceId() == scheduleID {
				count++
			}
		}
		return count == 1
	}, "deduped schedule wakeup")
}

func TestScheduleRunNowPreservesApprovalConveyor(t *testing.T) {
	t.Parallel()

	planner := &staticPlannerFunc{fn: func(_ context.Context, req agent.PlanRequest) (agent.PlanResult, error) {
		if req.UserMessage == "schedule goal requires approval" {
			for _, msg := range req.History {
				if strings.Contains(msg.Content, "[tool_result:run_bash]") {
					return agent.PlanResult{AssistantMessage: "Scheduled run completed."}, nil
				}
			}
			return agent.PlanResult{
				AssistantMessage: "Need approval for scheduled command.",
				ProposedActions: []agent.ProposedAction{{
					Tool:          "run_bash",
					Args:          []byte(`{"command":"echo schedule approval"}`),
					Justification: "Scheduled work needs shell command.",
					RiskClass:     "HIGH",
				}},
			}, nil
		}
		return agent.PlanResult{AssistantMessage: "ok"}, nil
	}}

	app := newTestAppWithPlanner(t, planner)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapConnectToken(t, srv.URL)
	httpClient := newAuthorizedHTTPClient(token)

	schedulesClient := protocolv1connect.NewSchedulesServiceClient(httpClient, srv.URL)
	jobsClient := protocolv1connect.NewJobsServiceClient(httpClient, srv.URL)
	approvalsClient := protocolv1connect.NewApprovalsServiceClient(httpClient, srv.URL)
	systemClient := protocolv1connect.NewSystemServiceClient(httpClient, srv.URL)

	createResp, err := schedulesClient.CreateSchedule(context.Background(), connect.NewRequest(&protocolv1.CreateScheduleRequest{
		Name:        "schedule goal requires approval",
		TriggerKind: protocolv1.ScheduleTriggerKind_SCHEDULE_TRIGGER_INTERVAL,
		TriggerSpec: "15m",
		Timezone:    "UTC",
	}))
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	scheduleID := createResp.Msg.GetItem().GetScheduleId()

	if _, err := schedulesClient.RunScheduleNow(context.Background(), connect.NewRequest(&protocolv1.RunScheduleNowRequest{ScheduleId: scheduleID})); err != nil {
		t.Fatalf("run schedule now: %v", err)
	}

	waitForCondition(t, 5*time.Second, func() bool {
		jobsResp, err := jobsClient.ListJobs(context.Background(), connect.NewRequest(&protocolv1.ListJobsRequest{}))
		if err != nil {
			return false
		}
		for _, item := range jobsResp.Msg.GetItems() {
			if item.GetTriggerSourceId() == scheduleID && item.GetStatus() == protocolv1.JobStatus_JOB_WAITING_APPROVAL {
				return true
			}
		}
		return false
	}, "schedule job waiting approval")

	pendingResp, err := approvalsClient.ListApprovals(context.Background(), connect.NewRequest(&protocolv1.ListApprovalsRequest{Status: protocolv1.ActionStatus_PENDING}))
	if err != nil {
		t.Fatalf("list pending approvals: %v", err)
	}
	if len(pendingResp.Msg.GetItems()) == 0 {
		t.Fatalf("expected pending approval from schedule-triggered job")
	}
	actionID := pendingResp.Msg.GetItems()[0].GetActionId()

	auditBefore, err := systemClient.ListAudit(context.Background(), connect.NewRequest(&protocolv1.ListAuditRequest{}))
	if err != nil {
		t.Fatalf("list audit before approval: %v", err)
	}
	for _, entry := range auditBefore.Msg.GetItems() {
		if entry.GetActionId() == actionID && entry.GetEventType() == "action_executed" {
			t.Fatalf("action executed before approval for %s", actionID)
		}
	}

	if _, err := approvalsClient.ApproveAction(context.Background(), connect.NewRequest(&protocolv1.ApproveActionRequest{ActionId: actionID})); err != nil {
		t.Fatalf("approve action: %v", err)
	}

	waitForCondition(t, 8*time.Second, func() bool {
		listResp, err := jobsClient.ListJobs(context.Background(), connect.NewRequest(&protocolv1.ListJobsRequest{}))
		if err != nil {
			return false
		}
		for _, item := range listResp.Msg.GetItems() {
			if item.GetTriggerSourceId() == scheduleID && item.GetStatus() == protocolv1.JobStatus_JOB_COMPLETED {
				return true
			}
		}
		return false
	}, "schedule job completion after approval")

	waitForCondition(t, 5*time.Second, func() bool {
		auditResp, err := systemClient.ListAudit(context.Background(), connect.NewRequest(&protocolv1.ListAuditRequest{}))
		if err != nil {
			return false
		}
		for _, entry := range auditResp.Msg.GetItems() {
			if entry.GetActionId() == actionID && entry.GetEventType() == "action_executed" {
				return true
			}
		}
		return false
	}, "schedule action executed after approval")
}

func TestSchedulesServiceCreateListAndUpdate(t *testing.T) {
	t.Parallel()

	app := newTestAppWithPlanner(t, &staticPlannerFunc{fn: func(_ context.Context, _ agent.PlanRequest) (agent.PlanResult, error) {
		return agent.PlanResult{AssistantMessage: "ok"}, nil
	}})
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapConnectToken(t, srv.URL)
	httpClient := newAuthorizedHTTPClient(token)

	schedulesClient := protocolv1connect.NewSchedulesServiceClient(httpClient, srv.URL)

	createResp, err := schedulesClient.CreateSchedule(context.Background(), connect.NewRequest(&protocolv1.CreateScheduleRequest{
		Name:        "Morning digest",
		TriggerKind: protocolv1.ScheduleTriggerKind_SCHEDULE_TRIGGER_INTERVAL,
		TriggerSpec: "15m",
		Timezone:    "UTC",
	}))
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	scheduleID := createResp.Msg.GetItem().GetScheduleId()
	if scheduleID == "" {
		t.Fatalf("expected create schedule response to include schedule_id")
	}

	listResp, err := schedulesClient.ListSchedules(context.Background(), connect.NewRequest(&protocolv1.ListSchedulesRequest{}))
	if err != nil {
		t.Fatalf("list schedules: %v", err)
	}
	if len(listResp.Msg.GetItems()) == 0 {
		t.Fatalf("expected at least one schedule in list")
	}

	found := false
	for _, item := range listResp.Msg.GetItems() {
		if item.GetScheduleId() == scheduleID {
			found = true
			if item.GetName() != "Morning digest" {
				t.Fatalf("expected schedule name %q, got %q", "Morning digest", item.GetName())
			}
		}
	}
	if !found {
		t.Fatalf("expected created schedule in list")
	}

	patch, err := structpb.NewStruct(map[string]any{
		"name":    "Updated digest",
		"enabled": false,
	})
	if err != nil {
		t.Fatalf("build patch: %v", err)
	}

	updateResp, err := schedulesClient.UpdateSchedule(context.Background(), connect.NewRequest(&protocolv1.UpdateScheduleRequest{
		ScheduleId: scheduleID,
		Patch:      patch,
	}))
	if err != nil {
		t.Fatalf("update schedule: %v", err)
	}
	if updateResp.Msg.GetItem().GetName() != "Updated digest" {
		t.Fatalf("expected updated name %q, got %q", "Updated digest", updateResp.Msg.GetItem().GetName())
	}
	if updateResp.Msg.GetItem().GetEnabled() {
		t.Fatalf("expected updated schedule to be disabled")
	}
	if updateResp.Msg.GetItem().GetNextRunAt() != nil {
		t.Fatalf("expected disabled schedule to clear next_run_at")
	}
}

func TestSystemServiceAgentMemoryReadAndUpdate(t *testing.T) {
	t.Parallel()

	app := newTestAppWithPlanner(t, &staticPlannerFunc{fn: func(_ context.Context, _ agent.PlanRequest) (agent.PlanResult, error) {
		return agent.PlanResult{AssistantMessage: "ok"}, nil
	}})
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapConnectToken(t, srv.URL)
	httpClient := newAuthorizedHTTPClient(token)
	systemClient := protocolv1connect.NewSystemServiceClient(httpClient, srv.URL)

	getBefore, err := systemClient.GetAgentMemory(context.Background(), connect.NewRequest(&protocolv1.GetAgentMemoryRequest{}))
	if err != nil {
		t.Fatalf("get agent memory before update: %v", err)
	}
	if strings.TrimSpace(getBefore.Msg.GetContent()) != "" {
		t.Fatalf("expected empty default memory content, got %q", getBefore.Msg.GetContent())
	}

	memoryContent := "# Memory\n\n- User prefers concise responses\n"
	updateResp, err := systemClient.UpdateAgentMemory(context.Background(), connect.NewRequest(&protocolv1.UpdateAgentMemoryRequest{
		Content: memoryContent,
	}))
	if err != nil {
		t.Fatalf("update agent memory: %v", err)
	}
	if updateResp.Msg.GetWrittenBytes() == 0 {
		t.Fatalf("expected written_bytes > 0")
	}
	if updateResp.Msg.GetUpdatedAt() == nil {
		t.Fatalf("expected updated_at in update response")
	}

	getAfter, err := systemClient.GetAgentMemory(context.Background(), connect.NewRequest(&protocolv1.GetAgentMemoryRequest{}))
	if err != nil {
		t.Fatalf("get agent memory after update: %v", err)
	}
	if getAfter.Msg.GetContent() != memoryContent {
		t.Fatalf("expected updated memory content %q, got %q", memoryContent, getAfter.Msg.GetContent())
	}
	if getAfter.Msg.GetUpdatedAt() == nil {
		t.Fatalf("expected updated_at in get response")
	}

	storedBytes, err := os.ReadFile(filepath.Join(app.workspaceRoot, "memory", "MEMORY.md"))
	if err != nil {
		t.Fatalf("read memory file from workspace: %v", err)
	}
	if string(storedBytes) != memoryContent {
		t.Fatalf("expected workspace memory file content %q, got %q", memoryContent, string(storedBytes))
	}
}

func TestSystemServiceHeartbeatConfigReadAndUpdate(t *testing.T) {
	t.Parallel()

	app := newTestAppWithPlanner(t, &staticPlannerFunc{fn: func(_ context.Context, _ agent.PlanRequest) (agent.PlanResult, error) {
		return agent.PlanResult{AssistantMessage: "ok"}, nil
	}})
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapConnectToken(t, srv.URL)
	httpClient := newAuthorizedHTTPClient(token)
	systemClient := protocolv1connect.NewSystemServiceClient(httpClient, srv.URL)

	getBefore, err := systemClient.GetHeartbeatConfig(context.Background(), connect.NewRequest(&protocolv1.GetHeartbeatConfigRequest{}))
	if err != nil {
		t.Fatalf("get heartbeat config before update: %v", err)
	}
	if getBefore.Msg.GetIntervalMinutes() == 0 {
		t.Fatalf("expected non-zero heartbeat interval in config")
	}
	if !strings.Contains(getBefore.Msg.GetTasksMarkdown(), "# Periodic Tasks") {
		t.Fatalf("expected default heartbeat tasks markdown, got %q", getBefore.Msg.GetTasksMarkdown())
	}

	updatedTasks := "# Periodic Tasks\n\n- Check urgent inbox messages\n"
	updateResp, err := systemClient.UpdateHeartbeatConfig(context.Background(), connect.NewRequest(&protocolv1.UpdateHeartbeatConfigRequest{
		Enabled:         true,
		IntervalMinutes: uint32(minScheduleInterval / time.Minute),
		TasksMarkdown:   updatedTasks,
	}))
	if err != nil {
		t.Fatalf("update heartbeat config: %v", err)
	}
	if !updateResp.Msg.GetEnabled() {
		t.Fatalf("expected heartbeat enabled after update")
	}
	if updateResp.Msg.GetIntervalMinutes() != uint32(minScheduleInterval/time.Minute) {
		t.Fatalf("expected interval %d, got %d", minScheduleInterval/time.Minute, updateResp.Msg.GetIntervalMinutes())
	}
	if updateResp.Msg.GetTasksMarkdown() != updatedTasks {
		t.Fatalf("expected updated tasks markdown %q, got %q", updatedTasks, updateResp.Msg.GetTasksMarkdown())
	}

	getAfter, err := systemClient.GetHeartbeatConfig(context.Background(), connect.NewRequest(&protocolv1.GetHeartbeatConfigRequest{}))
	if err != nil {
		t.Fatalf("get heartbeat config after update: %v", err)
	}
	if !getAfter.Msg.GetEnabled() {
		t.Fatalf("expected heartbeat config to stay enabled")
	}
	if getAfter.Msg.GetIntervalMinutes() != uint32(minScheduleInterval/time.Minute) {
		t.Fatalf("expected persisted interval %d, got %d", minScheduleInterval/time.Minute, getAfter.Msg.GetIntervalMinutes())
	}
	if getAfter.Msg.GetTasksMarkdown() != updatedTasks {
		t.Fatalf("expected persisted tasks markdown %q, got %q", updatedTasks, getAfter.Msg.GetTasksMarkdown())
	}

	policyResp, err := systemClient.GetPolicySummary(context.Background(), connect.NewRequest(&protocolv1.GetPolicySummaryRequest{}))
	if err != nil {
		t.Fatalf("get policy summary: %v", err)
	}
	policyFields := policyResp.Msg.GetSummary().GetFields()
	if !policyFields["heartbeat_enabled"].GetBoolValue() {
		t.Fatalf("expected policy summary heartbeat_enabled=true after config update")
	}
	if got := policyFields["heartbeat_interval_minutes"].GetNumberValue(); got != float64(minScheduleInterval/time.Minute) {
		t.Fatalf("expected policy summary heartbeat_interval_minutes=%d, got %v", minScheduleInterval/time.Minute, got)
	}

	heartbeatBytes, err := os.ReadFile(filepath.Join(app.workspaceRoot, "HEARTBEAT.md"))
	if err != nil {
		t.Fatalf("read heartbeat file from workspace: %v", err)
	}
	if string(heartbeatBytes) != updatedTasks {
		t.Fatalf("expected heartbeat file content %q, got %q", updatedTasks, string(heartbeatBytes))
	}
}

func TestSystemServiceHeartbeatConfigRollsBackTasksWhenPersistFails(t *testing.T) {
	t.Parallel()

	app := newTestAppWithPlanner(t, &staticPlannerFunc{fn: func(_ context.Context, _ agent.PlanRequest) (agent.PlanResult, error) {
		return agent.PlanResult{AssistantMessage: "ok"}, nil
	}})
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapConnectToken(t, srv.URL)
	httpClient := newAuthorizedHTTPClient(token)
	systemClient := protocolv1connect.NewSystemServiceClient(httpClient, srv.URL)

	getBefore, err := systemClient.GetHeartbeatConfig(context.Background(), connect.NewRequest(&protocolv1.GetHeartbeatConfigRequest{}))
	if err != nil {
		t.Fatalf("get heartbeat config before failed update: %v", err)
	}
	heartbeatPath := filepath.Join(app.workspaceRoot, "HEARTBEAT.md")
	originalHeartbeatBytes, err := os.ReadFile(heartbeatPath)
	if err != nil {
		t.Fatalf("read heartbeat file before failed update: %v", err)
	}

	if _, err := app.db.Exec(`DROP TABLE runtime_settings`); err != nil {
		t.Fatalf("drop runtime_settings table: %v", err)
	}

	_, err = systemClient.UpdateHeartbeatConfig(context.Background(), connect.NewRequest(&protocolv1.UpdateHeartbeatConfigRequest{
		Enabled:         !getBefore.Msg.GetEnabled(),
		IntervalMinutes: uint32(minScheduleInterval / time.Minute),
		TasksMarkdown:   "# Periodic Tasks\n\n- This update should roll back\n",
	}))
	if err == nil {
		t.Fatalf("expected update heartbeat config to fail when runtime_settings table is missing")
	}
	if code := connect.CodeOf(err); code != connect.CodeInternal {
		t.Fatalf("expected internal error code, got %s", code)
	}

	getAfter, err := systemClient.GetHeartbeatConfig(context.Background(), connect.NewRequest(&protocolv1.GetHeartbeatConfigRequest{}))
	if err != nil {
		t.Fatalf("get heartbeat config after failed update: %v", err)
	}
	if getAfter.Msg.GetEnabled() != getBefore.Msg.GetEnabled() {
		t.Fatalf("expected heartbeat enabled to remain %v, got %v", getBefore.Msg.GetEnabled(), getAfter.Msg.GetEnabled())
	}
	if getAfter.Msg.GetIntervalMinutes() != getBefore.Msg.GetIntervalMinutes() {
		t.Fatalf("expected heartbeat interval to remain %d, got %d", getBefore.Msg.GetIntervalMinutes(), getAfter.Msg.GetIntervalMinutes())
	}
	if getAfter.Msg.GetTasksMarkdown() != string(originalHeartbeatBytes) {
		t.Fatalf("expected heartbeat tasks markdown to roll back to original content")
	}

	currentHeartbeatBytes, err := os.ReadFile(heartbeatPath)
	if err != nil {
		t.Fatalf("read heartbeat file after failed update: %v", err)
	}
	if string(currentHeartbeatBytes) != string(originalHeartbeatBytes) {
		t.Fatalf("expected heartbeat file to roll back to original content")
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
