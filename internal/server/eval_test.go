//go:build eval

package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/lox/pincer/internal/agent"
)

func TestEvalEndToEndApprovalFlow(t *testing.T) {
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		t.Skip("OPENROUTER_API_KEY not set")
	}

	baseURL := os.Getenv("OPENROUTER_BASE_URL")
	primaryModel := os.Getenv("PINCER_MODEL_PRIMARY")
	if primaryModel == "" {
		primaryModel = "anthropic/claude-opus-4.6"
	}
	fallbackModel := os.Getenv("PINCER_MODEL_FALLBACK")

	planner, err := agent.NewOpenAIPlanner(agent.OpenAIPlannerConfig{
		APIKey:        apiKey,
		BaseURL:       baseURL,
		PrimaryModel:  primaryModel,
		FallbackModel: fallbackModel,
		HTTPClient:    &http.Client{Timeout: 60 * time.Second},
		SOULPath:      "../../SOUL.md",
	})
	if err != nil {
		t.Fatalf("create planner: %v", err)
	}

	app := newTestAppWithPlanner(t, planner)
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	token := bootstrapAuthToken(t, srv.URL)
	threadID := createThread(t, srv.URL, token)

	response := postMessageResponse(t, srv.URL, token, threadID, "Please run bash command pwd and require approval")
	if response.ActionID == "" {
		t.Fatalf("expected a proposed action, got none (assistant_message=%q)", response.AssistantMessage)
	}
	actionID := response.ActionID
	t.Logf("thread_id=%s action_id=%s", threadID, actionID)

	approveAction(t, srv.URL, token, actionID)

	executed := waitForApprovalStatus(t, srv.URL, token, "executed", actionID, 30*time.Second)
	if len(executed) == 0 {
		t.Fatalf("timeout waiting for action %s to reach executed status", actionID)
	}

	events := listAudit(t, srv.URL, token)
	for _, eventType := range []string{"action_proposed", "action_approved", "action_executed"} {
		if countAuditEvents(events, actionID, eventType) != 1 {
			t.Fatalf("expected 1 %s audit event for action %s", eventType, actionID)
		}
	}

	t.Log("eval ok")
}
