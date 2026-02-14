package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const defaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"
const defaultSOULPath = "SOUL.md"

var (
	ErrInvalidModelOutput = errors.New("invalid model output")
	ErrFailedModelOutput  = errors.New("failed model output")
)

type Message struct {
	Role    string
	Content string
}

type PlanRequest struct {
	ThreadID    string
	UserMessage string
	History     []Message
}

type ProposedAction struct {
	Tool          string          `json:"tool"`
	Args          json.RawMessage `json:"args"`
	Justification string          `json:"justification"`
	RiskClass     string          `json:"risk_class,omitempty"`
}

type PlanResult struct {
	AssistantMessage string           `json:"assistant_message"`
	ProposedActions  []ProposedAction `json:"proposed_actions"`
}

type Planner interface {
	Plan(ctx context.Context, req PlanRequest) (PlanResult, error)
}

type staticPlanner struct{}

func NewStaticPlanner() Planner {
	return staticPlanner{}
}

func (staticPlanner) Plan(_ context.Context, _ PlanRequest) (PlanResult, error) {
	return PlanResult{
		AssistantMessage: "No external actions were proposed.",
		ProposedActions:  []ProposedAction{},
	}, nil
}

type fallbackPlanner struct {
	primary  Planner
	fallback Planner
}

func NewFallbackPlanner(primary, fallback Planner) Planner {
	switch {
	case primary == nil:
		return fallback
	case fallback == nil:
		return primary
	default:
		return fallbackPlanner{primary: primary, fallback: fallback}
	}
}

func (p fallbackPlanner) Plan(ctx context.Context, req PlanRequest) (PlanResult, error) {
	result, err := p.primary.Plan(ctx, req)
	if err == nil {
		return result, nil
	}
	return p.fallback.Plan(ctx, req)
}

type OpenAIPlannerConfig struct {
	APIKey        string
	BaseURL       string
	PrimaryModel  string
	FallbackModel string
	HTTPClient    *http.Client
	UserAgent     string
	SOULPrompt    string
	SOULPath      string
}

type OpenAIPlanner struct {
	apiKey        string
	baseURL       string
	primaryModel  string
	fallbackModel string
	httpClient    *http.Client
	userAgent     string
	soulPrompt    string
}

func NewOpenAIPlanner(cfg OpenAIPlannerConfig) (*OpenAIPlanner, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("api key is required")
	}
	if strings.TrimSpace(cfg.PrimaryModel) == "" {
		return nil, errors.New("primary model is required")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 45 * time.Second}
	}

	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = defaultOpenRouterBaseURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	soulPrompt := strings.TrimSpace(cfg.SOULPrompt)
	if soulPrompt == "" {
		soulPath := strings.TrimSpace(cfg.SOULPath)
		if soulPath == "" {
			soulPath = defaultSOULPath
		}

		loaded, err := loadSOULPromptFile(soulPath)
		if err != nil {
			return nil, fmt.Errorf("read SOUL prompt: %w", err)
		}
		soulPrompt = loaded
	}

	return &OpenAIPlanner{
		apiKey:        strings.TrimSpace(cfg.APIKey),
		baseURL:       baseURL,
		primaryModel:  strings.TrimSpace(cfg.PrimaryModel),
		fallbackModel: strings.TrimSpace(cfg.FallbackModel),
		httpClient:    cfg.HTTPClient,
		userAgent:     strings.TrimSpace(cfg.UserAgent),
		soulPrompt:    soulPrompt,
	}, nil
}

func (p *OpenAIPlanner) Plan(ctx context.Context, req PlanRequest) (PlanResult, error) {
	models := []string{p.primaryModel}
	if p.fallbackModel != "" && p.fallbackModel != p.primaryModel {
		models = append(models, p.fallbackModel)
	}

	var lastErr error
	for _, model := range models {
		result, err := p.planWithModel(ctx, model, req, false)
		if err == nil {
			return result, nil
		}
		lastErr = err

		if errors.Is(err, ErrInvalidModelOutput) {
			result, repairErr := p.planWithModel(ctx, model, req, true)
			if repairErr == nil {
				return result, nil
			}
			lastErr = repairErr
		}
	}

	if lastErr == nil {
		lastErr = ErrFailedModelOutput
	}
	return PlanResult{}, fmt.Errorf("%w: %v", ErrFailedModelOutput, lastErr)
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatCompletionRequest struct {
	Model          string                 `json:"model"`
	Messages       []openAIMessage        `json:"messages"`
	ResponseFormat map[string]string      `json:"response_format,omitempty"`
	Temperature    float64                `json:"temperature"`
	Extra          map[string]interface{} `json:"extra_body,omitempty"`
}

type openAIChatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func (p *OpenAIPlanner) planWithModel(ctx context.Context, model string, req PlanRequest, repair bool) (PlanResult, error) {
	messages := []openAIMessage{
		{
			Role: "system",
			Content: "You are the planning harness for Pincer. Return only a single JSON object with keys assistant_message and proposed_actions. " +
				"proposed_actions must be an array of objects with tool, args (JSON object), justification, and optional risk_class. " +
				"Only return non-empty proposed_actions when the user explicitly asked for an external action or workflow. " +
				"For shell command requests, propose tool run_bash with args containing command and optional cwd. " +
				"Never return markdown or code fences.",
		},
	}
	if p.soulPrompt != "" {
		messages = append(messages, openAIMessage{
			Role: "system",
			Content: "Apply the following SOUL guidance for style and phrasing while still obeying the required JSON response schema and safety constraints:\n" +
				p.soulPrompt,
		})
	}
	if repair {
		messages = append(messages, openAIMessage{
			Role:    "system",
			Content: "Your previous response was invalid. Return valid JSON only and follow the exact schema.",
		})
	}
	messages = append(messages, openAIMessage{
		Role:    "user",
		Content: buildPlannerPrompt(req),
	})

	payload := openAIChatCompletionRequest{
		Model:          model,
		Messages:       messages,
		ResponseFormat: map[string]string{"type": "json_object"},
		Temperature:    0.2,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return PlanResult{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return PlanResult{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	if p.userAgent != "" {
		httpReq.Header.Set("User-Agent", p.userAgent)
	}

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return PlanResult{}, err
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return PlanResult{}, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return PlanResult{}, fmt.Errorf("chat completion status %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}

	var parsed openAIChatCompletionResponse
	if err := json.Unmarshal(responseBody, &parsed); err != nil {
		return PlanResult{}, fmt.Errorf("decode completion response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return PlanResult{}, ErrInvalidModelOutput
	}

	result, err := parsePlanResult(parsed.Choices[0].Message.Content)
	if err != nil {
		return PlanResult{}, err
	}
	return result, nil
}

func buildPlannerPrompt(req PlanRequest) string {
	var b strings.Builder
	b.WriteString("Thread ID: ")
	b.WriteString(req.ThreadID)
	b.WriteString("\n")

	if len(req.History) > 0 {
		b.WriteString("Recent messages:\n")
		for _, msg := range req.History {
			b.WriteString("- ")
			b.WriteString(msg.Role)
			b.WriteString(": ")
			b.WriteString(msg.Content)
			b.WriteString("\n")
		}
	}

	b.WriteString("Latest user message:\n")
	b.WriteString(req.UserMessage)
	return b.String()
}

func parsePlanResult(content string) (PlanResult, error) {
	clean := strings.TrimSpace(content)
	if clean == "" {
		return PlanResult{}, ErrInvalidModelOutput
	}

	var result PlanResult
	if err := json.Unmarshal([]byte(clean), &result); err != nil {
		jsonObject, extractErr := extractLikelyJSONObject(clean)
		if extractErr != nil {
			return PlanResult{}, ErrInvalidModelOutput
		}
		if err := json.Unmarshal([]byte(jsonObject), &result); err != nil {
			return PlanResult{}, ErrInvalidModelOutput
		}
	}

	return normalizePlanResult(result)
}

func normalizePlanResult(result PlanResult) (PlanResult, error) {
	assistant := strings.TrimSpace(result.AssistantMessage)
	if assistant == "" {
		return PlanResult{}, ErrInvalidModelOutput
	}

	normalized := make([]ProposedAction, 0, len(result.ProposedActions))
	for _, action := range result.ProposedActions {
		tool := strings.TrimSpace(action.Tool)
		if tool == "" {
			return PlanResult{}, ErrInvalidModelOutput
		}

		args := action.Args
		if !isJSONObject(args) {
			args = json.RawMessage(`{}`)
		}

		justification := strings.TrimSpace(action.Justification)
		if justification == "" {
			justification = "Proposed by planning model."
		}

		normalized = append(normalized, ProposedAction{
			Tool:          tool,
			Args:          args,
			Justification: justification,
			RiskClass:     strings.ToUpper(strings.TrimSpace(action.RiskClass)),
		})
	}

	return PlanResult{
		AssistantMessage: assistant,
		ProposedActions:  normalized,
	}, nil
}

func extractLikelyJSONObject(content string) (string, error) {
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return "", ErrInvalidModelOutput
	}
	return strings.TrimSpace(content[start : end+1]), nil
}

func isJSONObject(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	if !json.Valid(raw) {
		return false
	}

	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return false
	}
	_, ok := decoded.(map[string]any)
	return ok
}

func loadSOULPromptFile(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(contents)), nil
}
