package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultVisionModel   = "anthropic/claude-opus-4.6"
	defaultVisionTimeout = 60 * time.Second
	maxDescriptionBytes  = 8 * 1024
)

// ImageDescribeArgs are the arguments for the image_describe tool call.
type ImageDescribeArgs struct {
	URL    string `json:"url"`
	Prompt string `json:"prompt,omitempty"`
}

// ImageDescriber analyzes images using a vision-capable model.
type ImageDescriber struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
}

// NewImageDescriber creates an ImageDescriber.
// baseURL should be the OpenAI-compatible API base (e.g. "https://openrouter.ai/api/v1").
func NewImageDescriber(apiKey, baseURL string) *ImageDescriber {
	if baseURL == "" {
		baseURL = defaultOpenRouterBaseURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	return &ImageDescriber{
		apiKey:     strings.TrimSpace(apiKey),
		baseURL:    baseURL,
		model:      defaultVisionModel,
		httpClient: &http.Client{Timeout: defaultVisionTimeout},
	}
}

// Describe sends an image URL to a vision model and returns its analysis.
func (d *ImageDescriber) Describe(ctx context.Context, args ImageDescribeArgs) (string, error) {
	imageURL := strings.TrimSpace(args.URL)
	if imageURL == "" {
		return "", fmt.Errorf("url is required")
	}

	prompt := strings.TrimSpace(args.Prompt)
	if prompt == "" {
		prompt = "Describe this image in detail."
	}

	// Build multimodal message with image_url content part.
	type contentPart struct {
		Type     string `json:"type"`
		Text     string `json:"text,omitempty"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url,omitempty"`
	}

	parts := []contentPart{
		{Type: "text", Text: prompt},
		{Type: "image_url", ImageURL: &struct {
			URL string `json:"url"`
		}{URL: imageURL}},
	}

	type message struct {
		Role    string        `json:"role"`
		Content []contentPart `json:"content"`
	}

	payload := struct {
		Model       string    `json:"model"`
		Messages    []message `json:"messages"`
		MaxTokens   int       `json:"max_tokens"`
		Temperature float64   `json:"temperature"`
	}{
		Model: d.model,
		Messages: []message{
			{Role: "user", Content: parts},
		},
		MaxTokens:   1024,
		Temperature: 0.2,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+d.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "Pincer/0.1 (image_describe)")

	resp, err := d.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("vision request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read vision response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("vision model status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content *string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("decode vision response: %w", err)
	}
	if len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content == nil {
		return "", fmt.Errorf("vision model returned no content")
	}

	output := *parsed.Choices[0].Message.Content
	if len(output) > maxDescriptionBytes {
		output = output[:maxDescriptionBytes] + "\n...[truncated]"
	}

	return output, nil
}
