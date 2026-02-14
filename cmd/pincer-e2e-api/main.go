package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	protocolv1 "github.com/lox/pincer/gen/proto/pincer/protocol/v1"
	"github.com/lox/pincer/gen/proto/pincer/protocol/v1/protocolv1connect"
)

func main() {
	baseURL := envOrDefault("PINCER_BASE_URL", "http://127.0.0.1:18080")
	authToken := strings.TrimSpace(os.Getenv("PINCER_AUTH_TOKEN"))
	message := envOrDefault("PINCER_E2E_MESSAGE", "Please run bash command pwd and require approval")

	ctx := context.Background()

	if authToken == "" {
		token, err := bootstrapToken(ctx, baseURL)
		if err != nil {
			fatalf("bootstrap token: %v", err)
		}
		authToken = token
	}

	httpClient := newAuthorizedHTTPClient(authToken)
	threadsClient := protocolv1connect.NewThreadsServiceClient(httpClient, baseURL)
	turnsClient := protocolv1connect.NewTurnsServiceClient(httpClient, baseURL)
	approvalsClient := protocolv1connect.NewApprovalsServiceClient(httpClient, baseURL)
	systemClient := protocolv1connect.NewSystemServiceClient(httpClient, baseURL)

	createResp, err := threadsClient.CreateThread(ctx, connect.NewRequest(&protocolv1.CreateThreadRequest{}))
	if err != nil {
		fatalf("create thread: %v", err)
	}
	threadID := createResp.Msg.GetThreadId()
	if threadID == "" {
		fatalf("create thread: empty thread_id")
	}

	turnResp, err := turnsClient.SendTurn(ctx, connect.NewRequest(&protocolv1.SendTurnRequest{
		ThreadId:    threadID,
		UserText:    message,
		TriggerType: protocolv1.TriggerType_CHAT_MESSAGE,
	}))
	if err != nil {
		fatalf("send turn: %v", err)
	}
	assistantMessage := turnResp.Msg.GetAssistantMessage()

	pendingResp, err := approvalsClient.ListApprovals(ctx, connect.NewRequest(&protocolv1.ListApprovalsRequest{
		Status: protocolv1.ActionStatus_PENDING,
	}))
	if err != nil {
		fatalf("list pending approvals: %v", err)
	}
	if len(pendingResp.Msg.GetItems()) < 1 {
		fatalf("expected at least one pending approval")
	}
	actionID := pendingResp.Msg.GetItems()[0].GetActionId()
	if actionID == "" {
		fatalf("pending approval missing action_id")
	}

	if _, err := approvalsClient.ApproveAction(ctx, connect.NewRequest(&protocolv1.ApproveActionRequest{
		ActionId: actionID,
	})); err != nil {
		fatalf("approve action: %v", err)
	}

	if err := waitForExecuted(ctx, approvalsClient, actionID, 30*time.Second); err != nil {
		fatalf("wait executed approval: %v", err)
	}

	auditResp, err := systemClient.ListAudit(ctx, connect.NewRequest(&protocolv1.ListAuditRequest{}))
	if err != nil {
		fatalf("list audit: %v", err)
	}
	for _, event := range []string{"action_proposed", "action_approved", "action_executed"} {
		if countAuditEvents(auditResp.Msg.GetItems(), actionID, event) != 1 {
			fatalf("missing audit event %q for action %s", event, actionID)
		}
	}

	fmt.Println("e2e ok")
	fmt.Printf("thread_id=%s\n", threadID)
	fmt.Printf("action_id=%s\n", actionID)
	fmt.Printf("assistant_message=%s\n", assistantMessage)
}

func bootstrapToken(ctx context.Context, baseURL string) (string, error) {
	authClient := protocolv1connect.NewAuthServiceClient(http.DefaultClient, baseURL)

	codeResp, err := authClient.CreatePairingCode(ctx, connect.NewRequest(&protocolv1.CreatePairingCodeRequest{}))
	if err != nil {
		return "", err
	}

	bindResp, err := authClient.BindPairingCode(ctx, connect.NewRequest(&protocolv1.BindPairingCodeRequest{
		Code:       codeResp.Msg.GetCode(),
		DeviceName: "e2e-api",
	}))
	if err != nil {
		return "", err
	}
	if bindResp.Msg.GetToken() == "" {
		return "", errors.New("bind pairing code returned empty token")
	}
	return bindResp.Msg.GetToken(), nil
}

func waitForExecuted(ctx context.Context, approvalsClient protocolv1connect.ApprovalsServiceClient, actionID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		resp, err := approvalsClient.ListApprovals(ctx, connect.NewRequest(&protocolv1.ListApprovalsRequest{
			Status: protocolv1.ActionStatus_EXECUTED,
		}))
		if err != nil {
			return err
		}
		for _, item := range resp.Msg.GetItems() {
			if item.GetActionId() == actionID {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for executed action %s", actionID)
		}
		time.Sleep(1 * time.Second)
	}
}

func countAuditEvents(items []*protocolv1.AuditEntry, actionID, eventType string) int {
	count := 0
	for _, item := range items {
		if item.GetActionId() == actionID && item.GetEventType() == eventType {
			count++
		}
	}
	return count
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
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
