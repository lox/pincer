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
	Thinking         string           `json:"thinking,omitempty"`
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
		AssistantMessage: "No LLM is configured. Set OPENROUTER_API_KEY on the backend to enable chat.",
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
	Content string `json:"content,omitempty"`
}

type openAIToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type openAITool struct {
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIChatCompletionRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	Tools       []openAITool    `json:"tools,omitempty"`
	Temperature float64         `json:"temperature"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIResponseMessage struct {
	Content          *string          `json:"content"`
	ReasoningContent *string          `json:"reasoning_content,omitempty"`
	ToolCalls        []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIChatCompletionResponse struct {
	Choices []struct {
		Message openAIResponseMessage `json:"message"`
	} `json:"choices"`
}

var knownTools = map[string]bool{
	"web_search":    true,
	"web_summarize": true,
	"web_fetch":     true,
	"run_bash":      true,
}

var plannerTools = []openAITool{
	{
		Type: "function",
		Function: openAIToolFunction{
			Name:        "web_search",
			Description: "Search the web for information. ALWAYS use this instead of run_bash with curl/wget for web searches.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {"type": "string", "description": "Search terms"},
					"max_results": {"type": "integer", "description": "Maximum number of results to return", "default": 5}
				},
				"required": ["query"]
			}`),
		},
	},
	{
		Type: "function",
		Function: openAIToolFunction{
			Name:        "web_summarize",
			Description: "Read and summarize content at any URL. Works with web pages, PDFs, YouTube videos, audio files, Word/PowerPoint documents, and more. ALWAYS use this instead of run_bash with curl/wget to read web content.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"url": {"type": "string", "description": "The URL to read and summarize"}
				},
				"required": ["url"]
			}`),
		},
	},
	{
		Type: "function",
		Function: openAIToolFunction{
			Name:        "web_fetch",
			Description: "Fetch the raw content of a URL. Returns the HTTP status code, content type, and body text. Use this to read specific pages, API responses, or documents. Subject to size limits and SSRF protections. Prefer web_summarize for long pages where a summary is sufficient.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"url": {"type": "string", "description": "The URL to fetch"}
				},
				"required": ["url"]
			}`),
		},
	},
	{
		Type: "function",
		Function: openAIToolFunction{
			Name:        "run_bash",
			Description: "Execute a shell command on the host. All shell commands require user approval before execution. Do NOT use for web access — use web_search or web_summarize instead.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"command": {"type": "string", "description": "The shell command to execute"},
					"cwd": {"type": "string", "description": "Working directory for the command"},
					"timeout_ms": {"type": "integer", "description": "Timeout in milliseconds", "default": 10000}
				},
				"required": ["command"]
			}`),
		},
	},
}

func (p *OpenAIPlanner) planWithModel(ctx context.Context, model string, req PlanRequest, repair bool) (PlanResult, error) {
	messages := []openAIMessage{
		{
			Role: "system",
			Content: "You are the planning harness for Pincer.\n\n" +
				"TOOL EXECUTION MODEL:\n" +
				"- When using a tool, call it via tool calling. Do not write JSON or describe tool calls in text.\n" +
				"- READ tools execute inline. Their results are appended to the conversation and you are called again to continue.\n" +
				"- You can chain multiple tool calls across rounds to gather information before giving a final answer.\n" +
				"- HIGH/WRITE/EXFILTRATION tools require user approval before execution.\n" +
				"- When no tools are needed, respond with your answer directly.\n\n" +
				"FORMATTING:\n" +
				"- Your responses support markdown. Use it for bold, lists, and links.\n" +
				"- Always render URLs as markdown links: [title](https://...). Never paste bare URLs.\n" +
				"- When summarizing fetched web content, preserve important source links from the original page. Include them inline next to the relevant claims so users can click through to the source.",
		},
	}
	if p.soulPrompt != "" {
		messages = append(messages, openAIMessage{
			Role: "system",
			Content: "Apply the following SOUL guidance for style and phrasing while still obeying safety constraints:\n" +
				p.soulPrompt,
		})
	}
	if repair {
		messages = append(messages, openAIMessage{
			Role:    "system",
			Content: "Your previous response was invalid. Please use the provided tools correctly or respond with a text message.",
		})
	}
	messages = append(messages, openAIMessage{
		Role:    "user",
		Content: buildPlannerPrompt(req),
	})

	payload := openAIChatCompletionRequest{
		Model:       model,
		Messages:    messages,
		Tools:       plannerTools,
		Temperature: 0.2,
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

	msg := parsed.Choices[0].Message
	content := ""
	if msg.Content != nil {
		content = *msg.Content
	}
	thinking := ""
	if msg.ReasoningContent != nil {
		thinking = *msg.ReasoningContent
	}
	result, err := parseToolCallResponse(content, msg.ToolCalls)
	if err != nil {
		return result, err
	}
	result.Thinking = thinking
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

func parseToolCallResponse(content string, toolCalls []openAIToolCall) (PlanResult, error) {
	actions := make([]ProposedAction, 0, len(toolCalls))
	for _, tc := range toolCalls {
		tool := strings.TrimSpace(tc.Function.Name)
		if tool == "" {
			continue
		}
		if !knownTools[tool] {
			return PlanResult{}, fmt.Errorf("%w: unknown tool %q", ErrInvalidModelOutput, tool)
		}

		args := json.RawMessage(tc.Function.Arguments)
		if !isJSONObject(args) {
			return PlanResult{}, fmt.Errorf("%w: invalid arguments for tool %q", ErrInvalidModelOutput, tool)
		}

		actions = append(actions, ProposedAction{
			Tool:          tool,
			Args:          args,
			Justification: justificationForAction(tool, args),
		})
	}

	assistant := strings.TrimSpace(content)
	if assistant == "" && len(actions) > 0 {
		assistant = "Working on it…"
	}
	if assistant == "" {
		return PlanResult{}, ErrInvalidModelOutput
	}

	return PlanResult{
		AssistantMessage: assistant,
		ProposedActions:  actions,
	}, nil
}

func justificationForAction(tool string, args json.RawMessage) string {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "run_bash":
		var a struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(args, &a) == nil && strings.TrimSpace(a.Command) != "" {
			cmd := strings.TrimSpace(a.Command)
			if len(cmd) > 200 {
				cmd = cmd[:200] + "…"
			}
			return fmt.Sprintf("Run: %s", cmd)
		}
	case "web_fetch":
		var a struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(args, &a) == nil && strings.TrimSpace(a.URL) != "" {
			return fmt.Sprintf("Fetch: %s", strings.TrimSpace(a.URL))
		}
	case "web_search":
		var a struct {
			Query string `json:"query"`
		}
		if json.Unmarshal(args, &a) == nil && strings.TrimSpace(a.Query) != "" {
			return fmt.Sprintf("Search: %s", strings.TrimSpace(a.Query))
		}
	case "web_summarize":
		var a struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(args, &a) == nil && strings.TrimSpace(a.URL) != "" {
			return fmt.Sprintf("Summarize: %s", strings.TrimSpace(a.URL))
		}
	}
	return "Proposed by planning model."
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
