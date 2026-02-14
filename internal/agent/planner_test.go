package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
)

func TestStaticPlannerReturnsNoActionsByDefault(t *testing.T) {
	t.Parallel()

	planner := NewStaticPlanner()
	result, err := planner.Plan(context.Background(), PlanRequest{
		ThreadID:    "thr_test",
		UserMessage: "hello",
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if result.AssistantMessage == "" {
		t.Fatalf("assistant message should not be empty")
	}
	if len(result.ProposedActions) != 0 {
		t.Fatalf("expected 0 proposed actions, got %d", len(result.ProposedActions))
	}
}

func TestOpenAIPlannerUsesRepairThenFallbackModel(t *testing.T) {
	t.Parallel()

	var (
		mu    sync.Mutex
		calls []string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req openAIChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		mu.Lock()
		calls = append(calls, req.Model)
		callIndex := len(calls)
		mu.Unlock()

		content := ""
		switch callIndex {
		case 1:
			content = "not json"
		case 2:
			content = "still not json"
		default:
			content = `{"assistant_message":"fallback worked","proposed_actions":[]}`
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": content}},
			},
		})
	}))
	defer srv.Close()

	planner, err := NewOpenAIPlanner(OpenAIPlannerConfig{
		APIKey:        "test-key",
		BaseURL:       srv.URL,
		PrimaryModel:  "primary-model",
		FallbackModel: "fallback-model",
		HTTPClient:    srv.Client(),
	})
	if err != nil {
		t.Fatalf("new planner: %v", err)
	}

	result, err := planner.Plan(context.Background(), PlanRequest{
		ThreadID:    "thr_test",
		UserMessage: "hello",
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if result.AssistantMessage != "fallback worked" {
		t.Fatalf("unexpected assistant message: %q", result.AssistantMessage)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"primary-model", "primary-model", "fallback-model"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("unexpected model call sequence: got=%v want=%v", calls, want)
	}
}

func TestParsePlanResultExtractsJSONFromWrappedContent(t *testing.T) {
	t.Parallel()

	content := "Here you go:\n```json\n{\"assistant_message\":\"ok\",\"proposed_actions\":[]}\n```"
	result, err := parsePlanResult(content)
	if err != nil {
		t.Fatalf("parse plan result: %v", err)
	}
	if result.AssistantMessage != "ok" {
		t.Fatalf("unexpected assistant message: %q", result.AssistantMessage)
	}
}
