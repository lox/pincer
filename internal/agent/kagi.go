package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	kagiBaseURL          = "https://kagi.com/api/v0"
	defaultKagiTimeout   = 30 * time.Second
	maxKagiSearchResults = 10
	maxKagiSummaryBytes  = 8 * 1024
)

// KagiClient provides web search and URL summarization via Kagi APIs.
type KagiClient struct {
	apiKey     string
	httpClient *http.Client
}

func NewKagiClient(apiKey string) *KagiClient {
	return &KagiClient{
		apiKey:     strings.TrimSpace(apiKey),
		httpClient: &http.Client{Timeout: defaultKagiTimeout},
	}
}

// SearchArgs are the arguments for web_search tool calls.
type SearchArgs struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
}

// SearchResult is a single search result.
type SearchResult struct {
	URL       string `json:"url"`
	Title     string `json:"title"`
	Snippet   string `json:"snippet,omitempty"`
	Published string `json:"published,omitempty"`
}

// Search calls the Kagi Search API.
func (k *KagiClient) Search(ctx context.Context, args SearchArgs) ([]SearchResult, error) {
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}

	limit := args.MaxResults
	if limit <= 0 || limit > maxKagiSearchResults {
		limit = maxKagiSearchResults
	}

	reqURL := fmt.Sprintf("%s/search?q=%s&limit=%d", kagiBaseURL, url.QueryEscape(query), limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bot "+k.apiKey)

	resp, err := k.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("kagi search status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var envelope struct {
		Data  []json.RawMessage `json:"data"`
		Error []struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode kagi response: %w", err)
	}
	if len(envelope.Error) > 0 {
		return nil, fmt.Errorf("kagi error %d: %s", envelope.Error[0].Code, envelope.Error[0].Msg)
	}

	results := make([]SearchResult, 0, len(envelope.Data))
	for _, raw := range envelope.Data {
		var obj struct {
			T         int    `json:"t"`
			URL       string `json:"url"`
			Title     string `json:"title"`
			Snippet   string `json:"snippet"`
			Published string `json:"published"`
		}
		if err := json.Unmarshal(raw, &obj); err != nil {
			continue
		}
		// t=0 is search result, t=1 is related searches (skip)
		if obj.T != 0 {
			continue
		}
		results = append(results, SearchResult{
			URL:       obj.URL,
			Title:     obj.Title,
			Snippet:   obj.Snippet,
			Published: obj.Published,
		})
	}
	return results, nil
}

// SummarizeArgs are the arguments for web_summarize tool calls.
type SummarizeArgs struct {
	URL string `json:"url"`
}

// SummarizeResult is the result of URL summarization.
type SummarizeResult struct {
	Output string `json:"output"`
	Tokens int    `json:"tokens"`
}

// Summarize calls the Kagi Universal Summarizer API.
func (k *KagiClient) Summarize(ctx context.Context, args SummarizeArgs) (SummarizeResult, error) {
	targetURL := strings.TrimSpace(args.URL)
	if targetURL == "" {
		return SummarizeResult{}, fmt.Errorf("url is required")
	}

	reqURL := fmt.Sprintf("%s/summarize?url=%s&summary_type=takeaway&engine=cecil", kagiBaseURL, url.QueryEscape(targetURL))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return SummarizeResult{}, err
	}
	req.Header.Set("Authorization", "Bot "+k.apiKey)

	resp, err := k.httpClient.Do(req)
	if err != nil {
		return SummarizeResult{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return SummarizeResult{}, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return SummarizeResult{}, fmt.Errorf("kagi summarize status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var envelope struct {
		Data struct {
			Output string `json:"output"`
			Tokens int    `json:"tokens"`
		} `json:"data"`
		Error []struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return SummarizeResult{}, fmt.Errorf("decode kagi response: %w", err)
	}
	if len(envelope.Error) > 0 {
		return SummarizeResult{}, fmt.Errorf("kagi error %d: %s", envelope.Error[0].Code, envelope.Error[0].Msg)
	}

	output := envelope.Data.Output
	if len(output) > maxKagiSummaryBytes {
		output = output[:maxKagiSummaryBytes] + "\n...[truncated]"
	}

	return SummarizeResult{
		Output: output,
		Tokens: envelope.Data.Tokens,
	}, nil
}
