package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

		switch callIndex {
		case 1, 2:
			// Return empty choices to trigger ErrInvalidModelOutput
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{
					{"message": map[string]any{"content": "fallback worked"}},
				},
			})
		}
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

func TestParseToolCallResponse(t *testing.T) {
	t.Parallel()

	t.Run("content only", func(t *testing.T) {
		result, err := parseToolCallResponse("Here is your answer.", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.AssistantMessage != "Here is your answer." {
			t.Fatalf("unexpected assistant message: %q", result.AssistantMessage)
		}
		if len(result.ProposedActions) != 0 {
			t.Fatalf("expected 0 actions, got %d", len(result.ProposedActions))
		}
	})

	t.Run("tool calls only", func(t *testing.T) {
		toolCalls := []openAIToolCall{
			{ID: "call_1", Type: "function", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "web_search", Arguments: `{"query":"test"}`}},
		}
		result, err := parseToolCallResponse("", toolCalls)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.AssistantMessage != "Working on it…" {
			t.Fatalf("unexpected assistant message: %q", result.AssistantMessage)
		}
		if len(result.ProposedActions) != 1 {
			t.Fatalf("expected 1 action, got %d", len(result.ProposedActions))
		}
		if result.ProposedActions[0].Tool != "web_search" {
			t.Fatalf("unexpected tool: %q", result.ProposedActions[0].Tool)
		}
	})

	t.Run("content and tool calls", func(t *testing.T) {
		toolCalls := []openAIToolCall{
			{ID: "call_1", Type: "function", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "run_bash", Arguments: `{"command":"ls"}`}},
		}
		result, err := parseToolCallResponse("Let me check that.", toolCalls)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.AssistantMessage != "Let me check that." {
			t.Fatalf("unexpected assistant message: %q", result.AssistantMessage)
		}
		if len(result.ProposedActions) != 1 {
			t.Fatalf("expected 1 action, got %d", len(result.ProposedActions))
		}
	})

	t.Run("empty content no tool calls", func(t *testing.T) {
		_, err := parseToolCallResponse("", nil)
		if err != ErrInvalidModelOutput {
			t.Fatalf("expected ErrInvalidModelOutput, got: %v", err)
		}
	})

	t.Run("invalid args returns error", func(t *testing.T) {
		toolCalls := []openAIToolCall{
			{ID: "call_1", Type: "function", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "web_search", Arguments: "not json"}},
		}
		_, err := parseToolCallResponse("ok", toolCalls)
		if !errors.Is(err, ErrInvalidModelOutput) {
			t.Fatalf("expected ErrInvalidModelOutput, got: %v", err)
		}
	})

	t.Run("unknown tool returns error", func(t *testing.T) {
		toolCalls := []openAIToolCall{
			{ID: "call_1", Type: "function", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "send_email", Arguments: `{"to":"x"}`}},
		}
		_, err := parseToolCallResponse("ok", toolCalls)
		if !errors.Is(err, ErrInvalidModelOutput) {
			t.Fatalf("expected ErrInvalidModelOutput, got: %v", err)
		}
	})
}

func TestOpenAIPlannerHandlesNullContentWithToolCalls(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate OpenAI response with content: null and tool_calls present.
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"content": null,
					"tool_calls": [{
						"id": "call_abc",
						"type": "function",
						"function": {
							"name": "web_search",
							"arguments": "{\"query\":\"golang testing\"}"
						}
					}]
				}
			}]
		}`))
	}))
	defer srv.Close()

	planner, err := NewOpenAIPlanner(OpenAIPlannerConfig{
		APIKey:       "test-key",
		BaseURL:      srv.URL,
		PrimaryModel: "test-model",
		HTTPClient:   srv.Client(),
	})
	if err != nil {
		t.Fatalf("new planner: %v", err)
	}

	result, err := planner.Plan(context.Background(), PlanRequest{
		ThreadID:    "thr_test",
		UserMessage: "search for golang testing",
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if result.AssistantMessage != "Working on it…" {
		t.Fatalf("unexpected assistant message: %q", result.AssistantMessage)
	}
	if len(result.ProposedActions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(result.ProposedActions))
	}
	if result.ProposedActions[0].Tool != "web_search" {
		t.Fatalf("unexpected tool: %q", result.ProposedActions[0].Tool)
	}
}

func TestOpenAIPlannerToolCallsWithInvalidArgsTriggersRepair(t *testing.T) {
	t.Parallel()

	var (
		mu    sync.Mutex
		calls int
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		callIndex := calls
		mu.Unlock()

		if callIndex == 1 {
			// First call: return tool call with invalid args
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {
						"content": null,
						"tool_calls": [{
							"id": "call_1",
							"type": "function",
							"function": {
								"name": "web_search",
								"arguments": "not valid json"
							}
						}]
					}
				}]
			}`))
		} else {
			// Repair call: return valid text response
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {
						"content": "Here is your answer."
					}
				}]
			}`))
		}
	}))
	defer srv.Close()

	planner, err := NewOpenAIPlanner(OpenAIPlannerConfig{
		APIKey:       "test-key",
		BaseURL:      srv.URL,
		PrimaryModel: "test-model",
		HTTPClient:   srv.Client(),
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
	if result.AssistantMessage != "Here is your answer." {
		t.Fatalf("unexpected assistant message: %q", result.AssistantMessage)
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("expected 2 calls (original + repair), got %d", calls)
	}
}

func TestOpenAIPlannerSendsToolsInRequest(t *testing.T) {
	t.Parallel()

	var (
		mu       sync.Mutex
		sawTools bool
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req openAIChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		mu.Lock()
		if len(req.Tools) == 4 {
			names := map[string]bool{}
			for _, t := range req.Tools {
				names[t.Function.Name] = true
			}
			if names["web_search"] && names["web_summarize"] && names["web_fetch"] && names["run_bash"] {
				sawTools = true
			}
		}
		mu.Unlock()

		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": "ok"}},
			},
		})
	}))
	defer srv.Close()

	planner, err := NewOpenAIPlanner(OpenAIPlannerConfig{
		APIKey:       "test-key",
		BaseURL:      srv.URL,
		PrimaryModel: "test-model",
		HTTPClient:   srv.Client(),
	})
	if err != nil {
		t.Fatalf("new planner: %v", err)
	}

	if _, err := planner.Plan(context.Background(), PlanRequest{
		ThreadID:    "thr_test",
		UserMessage: "hello",
	}); err != nil {
		t.Fatalf("plan: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !sawTools {
		t.Fatalf("expected request to include all 4 tool definitions")
	}
}

func TestOpenAIPlannerIncludesSOULPromptWhenConfigured(t *testing.T) {
	t.Parallel()

	const soul = "Be concise. Keep simple answers to one line."

	var (
		mu      sync.Mutex
		sawSOUL bool
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req openAIChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		for _, msg := range req.Messages {
			if msg.Role == "system" && strings.Contains(msg.Content, soul) {
				mu.Lock()
				sawSOUL = true
				mu.Unlock()
				break
			}
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": "ok"}},
			},
		})
	}))
	defer srv.Close()

	planner, err := NewOpenAIPlanner(OpenAIPlannerConfig{
		APIKey:       "test-key",
		BaseURL:      srv.URL,
		PrimaryModel: "primary-model",
		HTTPClient:   srv.Client(),
		SOULPrompt:   soul,
	})
	if err != nil {
		t.Fatalf("new planner: %v", err)
	}

	if _, err := planner.Plan(context.Background(), PlanRequest{
		ThreadID:    "thr_test",
		UserMessage: "hello",
	}); err != nil {
		t.Fatalf("plan: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !sawSOUL {
		t.Fatalf("expected planner request to include SOUL guidance")
	}
}

func TestOpenAIPlannerLoadsSOULPromptFromFile(t *testing.T) {
	t.Parallel()

	const soul = "Answer directly and keep it brief."

	dir := t.TempDir()
	soulPath := filepath.Join(dir, "SOUL.md")
	if err := os.WriteFile(soulPath, []byte(soul), 0o644); err != nil {
		t.Fatalf("write SOUL.md: %v", err)
	}

	var (
		mu      sync.Mutex
		sawSOUL bool
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req openAIChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		for _, msg := range req.Messages {
			if msg.Role == "system" && strings.Contains(msg.Content, soul) {
				mu.Lock()
				sawSOUL = true
				mu.Unlock()
				break
			}
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": "ok"}},
			},
		})
	}))
	defer srv.Close()

	planner, err := NewOpenAIPlanner(OpenAIPlannerConfig{
		APIKey:       "test-key",
		BaseURL:      srv.URL,
		PrimaryModel: "primary-model",
		HTTPClient:   srv.Client(),
		SOULPath:     soulPath,
	})
	if err != nil {
		t.Fatalf("new planner: %v", err)
	}

	if _, err := planner.Plan(context.Background(), PlanRequest{
		ThreadID:    "thr_test",
		UserMessage: "hello",
	}); err != nil {
		t.Fatalf("plan: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !sawSOUL {
		t.Fatalf("expected planner request to include SOUL prompt loaded from file")
	}
}
