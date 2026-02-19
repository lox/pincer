package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	gmailAPIBase        = "https://gmail.googleapis.com/gmail/v1"
	defaultGmailTimeout      = 30 * time.Second
	maxGmailResults          = 20
	maxGmailBodyBytes        = 32 * 1024
	maxGmailAttachmentBytes  = 25 * 1024 * 1024 // 25 MB (Gmail's attachment limit)
)

// OAuthToken represents a stored OAuth token.
type OAuthToken struct {
	Identity     string    `json:"identity"`
	Provider     string    `json:"provider"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
	Scopes       []string  `json:"scopes,omitempty"`
}

// IsExpired returns true if the token's expiry has passed.
func (t OAuthToken) IsExpired() bool {
	if t.Expiry.IsZero() {
		return false
	}
	return time.Now().After(t.Expiry)
}

// GmailSearchArgs are the arguments for gmail_search tool calls.
type GmailSearchArgs struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
}

// GmailSearchResult is a single search result (message header summary).
type GmailSearchResult struct {
	MessageID string `json:"message_id"`
	ThreadID  string `json:"thread_id"`
	Subject   string `json:"subject"`
	From      string `json:"from"`
	To        string `json:"to"`
	Date      string `json:"date"`
	Snippet   string `json:"snippet"`
}

// GmailReadArgs are the arguments for gmail_read tool calls.
type GmailReadArgs struct {
	MessageID string `json:"message_id"`
}

// AttachmentMeta describes a Gmail attachment without its content.
type AttachmentMeta struct {
	AttachmentID string `json:"attachment_id"`
	MessageID    string `json:"message_id"`
	Filename     string `json:"filename"`
	MimeType     string `json:"mime_type"`
	Size         int    `json:"size"`
}

// GmailReadResult is the full content of a single email.
type GmailReadResult struct {
	MessageID   string           `json:"message_id"`
	ThreadID    string           `json:"thread_id"`
	Subject     string           `json:"subject"`
	From        string           `json:"from"`
	To          string           `json:"to"`
	Cc          string           `json:"cc,omitempty"`
	Date        string           `json:"date"`
	Body        string           `json:"body"`
	Truncated   bool             `json:"truncated"`
	Labels      []string         `json:"labels,omitempty"`
	Attachments []AttachmentMeta `json:"attachments,omitempty"`
}

// GmailGetThreadArgs are the arguments for gmail_get_thread tool calls.
type GmailGetThreadArgs struct {
	ThreadID string `json:"thread_id"`
}

// GmailGetThreadResult contains all messages in a thread.
type GmailGetThreadResult struct {
	ThreadID string              `json:"thread_id"`
	Messages []GmailThreadMessage `json:"messages"`
}

// GmailThreadMessage is a summary of one message within a thread.
type GmailThreadMessage struct {
	MessageID   string           `json:"message_id"`
	From        string           `json:"from"`
	To          string           `json:"to"`
	Date        string           `json:"date"`
	Snippet     string           `json:"snippet"`
	Body        string           `json:"body"`
	Truncated   bool             `json:"truncated,omitempty"`
	Attachments []AttachmentMeta `json:"attachments,omitempty"`
}

// GmailCreateDraftArgs are the arguments for gmail_create_draft tool calls.
type GmailCreateDraftArgs struct {
	To       string `json:"to"`
	Subject  string `json:"subject"`
	Body     string `json:"body"`
	Cc       string `json:"cc,omitempty"`
	ReplyTo  string `json:"reply_to,omitempty"`  // message ID to reply to
	ThreadID string `json:"thread_id,omitempty"` // thread to attach draft to
}

// GmailCreateDraftResult is returned after creating a draft.
type GmailCreateDraftResult struct {
	DraftID   string `json:"draft_id"`
	MessageID string `json:"message_id"`
}

// GmailSendDraftArgs are the arguments for gmail_send_draft tool calls.
type GmailSendDraftArgs struct {
	DraftID string `json:"draft_id"`
}

// GmailSendDraftResult is returned after sending a draft.
type GmailSendDraftResult struct {
	MessageID string `json:"message_id"`
	ThreadID  string `json:"thread_id"`
}

// GmailClient provides Gmail API operations using OAuth access tokens.
type GmailClient struct {
	httpClient *http.Client
}

// NewGmailClient creates a GmailClient.
func NewGmailClient() *GmailClient {
	return &GmailClient{
		httpClient: &http.Client{Timeout: defaultGmailTimeout},
	}
}

// Search searches Gmail messages matching a query.
func (g *GmailClient) Search(ctx context.Context, accessToken string, args GmailSearchArgs) ([]GmailSearchResult, error) {
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}

	limit := args.MaxResults
	if limit <= 0 || limit > maxGmailResults {
		limit = maxGmailResults
	}

	// Step 1: List message IDs matching the query.
	listURL := fmt.Sprintf("%s/users/me/messages?q=%s&maxResults=%d", gmailAPIBase, url.QueryEscape(query), limit)
	listBody, err := g.doGet(ctx, accessToken, listURL)
	if err != nil {
		return nil, fmt.Errorf("gmail list: %w", err)
	}

	var listResp struct {
		Messages []struct {
			ID       string `json:"id"`
			ThreadID string `json:"threadId"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(listBody, &listResp); err != nil {
		return nil, fmt.Errorf("decode gmail list: %w", err)
	}

	if len(listResp.Messages) == 0 {
		return []GmailSearchResult{}, nil
	}

	// Step 2: Fetch metadata for each message.
	results := make([]GmailSearchResult, 0, len(listResp.Messages))
	for _, msg := range listResp.Messages {
		metaURL := fmt.Sprintf("%s/users/me/messages/%s?format=metadata&metadataHeaders=Subject&metadataHeaders=From&metadataHeaders=To&metadataHeaders=Date", gmailAPIBase, msg.ID)
		metaBody, err := g.doGet(ctx, accessToken, metaURL)
		if err != nil {
			continue // skip individual failures
		}

		var metaResp gmailMessageResponse
		if err := json.Unmarshal(metaBody, &metaResp); err != nil {
			continue
		}

		results = append(results, GmailSearchResult{
			MessageID: metaResp.ID,
			ThreadID:  metaResp.ThreadID,
			Subject:   metaResp.headerValue("Subject"),
			From:      metaResp.headerValue("From"),
			To:        metaResp.headerValue("To"),
			Date:      metaResp.headerValue("Date"),
			Snippet:   metaResp.Snippet,
		})
	}

	return results, nil
}

// Read fetches the full content of a single email.
func (g *GmailClient) Read(ctx context.Context, accessToken string, args GmailReadArgs) (GmailReadResult, error) {
	messageID := strings.TrimSpace(args.MessageID)
	if messageID == "" {
		return GmailReadResult{}, fmt.Errorf("message_id is required")
	}

	msgURL := fmt.Sprintf("%s/users/me/messages/%s?format=full", gmailAPIBase, url.PathEscape(messageID))
	body, err := g.doGet(ctx, accessToken, msgURL)
	if err != nil {
		return GmailReadResult{}, fmt.Errorf("gmail get message: %w", err)
	}

	var resp gmailMessageResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return GmailReadResult{}, fmt.Errorf("decode gmail message: %w", err)
	}

	textBody := resp.extractTextBody()
	truncated := false
	if len(textBody) > maxGmailBodyBytes {
		textBody = textBody[:maxGmailBodyBytes]
		truncated = true
	}

	return GmailReadResult{
		MessageID:   resp.ID,
		ThreadID:    resp.ThreadID,
		Subject:     resp.headerValue("Subject"),
		From:        resp.headerValue("From"),
		To:          resp.headerValue("To"),
		Cc:          resp.headerValue("Cc"),
		Date:        resp.headerValue("Date"),
		Body:        textBody,
		Truncated:   truncated,
		Labels:      resp.LabelIDs,
		Attachments: resp.extractAttachments(resp.ID),
	}, nil
}

// GetThread fetches all messages in a Gmail thread.
func (g *GmailClient) GetThread(ctx context.Context, accessToken string, args GmailGetThreadArgs) (GmailGetThreadResult, error) {
	threadID := strings.TrimSpace(args.ThreadID)
	if threadID == "" {
		return GmailGetThreadResult{}, fmt.Errorf("thread_id is required")
	}

	threadURL := fmt.Sprintf("%s/users/me/threads/%s?format=full", gmailAPIBase, url.PathEscape(threadID))
	body, err := g.doGet(ctx, accessToken, threadURL)
	if err != nil {
		return GmailGetThreadResult{}, fmt.Errorf("gmail get thread: %w", err)
	}

	var resp struct {
		ID       string                 `json:"id"`
		Messages []gmailMessageResponse `json:"messages"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return GmailGetThreadResult{}, fmt.Errorf("decode gmail thread: %w", err)
	}

	messages := make([]GmailThreadMessage, 0, len(resp.Messages))
	for _, msg := range resp.Messages {
		textBody := msg.extractTextBody()
		truncated := false
		if len(textBody) > maxGmailBodyBytes {
			textBody = textBody[:maxGmailBodyBytes]
			truncated = true
		}
		messages = append(messages, GmailThreadMessage{
			MessageID:   msg.ID,
			From:        msg.headerValue("From"),
			To:          msg.headerValue("To"),
			Date:        msg.headerValue("Date"),
			Snippet:     msg.Snippet,
			Body:        textBody,
			Truncated:   truncated,
			Attachments: msg.extractAttachments(msg.ID),
		})
	}

	return GmailGetThreadResult{
		ThreadID: resp.ID,
		Messages: messages,
	}, nil
}

// CreateDraft creates a draft email.
func (g *GmailClient) CreateDraft(ctx context.Context, accessToken string, args GmailCreateDraftArgs) (GmailCreateDraftResult, error) {
	to := strings.TrimSpace(args.To)
	if to == "" {
		return GmailCreateDraftResult{}, fmt.Errorf("to is required")
	}
	subject := strings.TrimSpace(args.Subject)
	body := strings.TrimSpace(args.Body)
	if body == "" {
		return GmailCreateDraftResult{}, fmt.Errorf("body is required")
	}

	// Build RFC 2822 message.
	var rawMsg strings.Builder
	rawMsg.WriteString("To: " + to + "\r\n")
	if cc := strings.TrimSpace(args.Cc); cc != "" {
		rawMsg.WriteString("Cc: " + cc + "\r\n")
	}
	rawMsg.WriteString("Subject: " + subject + "\r\n")
	if args.ReplyTo != "" {
		rawMsg.WriteString("In-Reply-To: " + args.ReplyTo + "\r\n")
		rawMsg.WriteString("References: " + args.ReplyTo + "\r\n")
	}
	rawMsg.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	rawMsg.WriteString("\r\n")
	rawMsg.WriteString(body)

	// URL-safe base64 encode the raw message.
	encoded := base64URLEncode([]byte(rawMsg.String()))

	draftPayload := map[string]any{
		"message": map[string]any{
			"raw": encoded,
		},
	}
	if args.ThreadID != "" {
		draftPayload["message"].(map[string]any)["threadId"] = args.ThreadID
	}

	payloadBytes, err := json.Marshal(draftPayload)
	if err != nil {
		return GmailCreateDraftResult{}, err
	}

	draftURL := fmt.Sprintf("%s/users/me/drafts", gmailAPIBase)
	respBody, err := g.doPost(ctx, accessToken, draftURL, payloadBytes)
	if err != nil {
		return GmailCreateDraftResult{}, fmt.Errorf("gmail create draft: %w", err)
	}

	var draftResp struct {
		ID      string `json:"id"`
		Message struct {
			ID string `json:"id"`
		} `json:"message"`
	}
	if err := json.Unmarshal(respBody, &draftResp); err != nil {
		return GmailCreateDraftResult{}, fmt.Errorf("decode gmail draft: %w", err)
	}

	return GmailCreateDraftResult{
		DraftID:   draftResp.ID,
		MessageID: draftResp.Message.ID,
	}, nil
}

// SendDraft sends an existing draft. This is an EXFILTRATION operation.
func (g *GmailClient) SendDraft(ctx context.Context, accessToken string, args GmailSendDraftArgs) (GmailSendDraftResult, error) {
	draftID := strings.TrimSpace(args.DraftID)
	if draftID == "" {
		return GmailSendDraftResult{}, fmt.Errorf("draft_id is required")
	}

	payload, err := json.Marshal(map[string]string{"id": draftID})
	if err != nil {
		return GmailSendDraftResult{}, err
	}

	sendURL := fmt.Sprintf("%s/users/me/drafts/send", gmailAPIBase)
	respBody, err := g.doPost(ctx, accessToken, sendURL, payload)
	if err != nil {
		return GmailSendDraftResult{}, fmt.Errorf("gmail send draft: %w", err)
	}

	var sendResp struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
	}
	if err := json.Unmarshal(respBody, &sendResp); err != nil {
		return GmailSendDraftResult{}, fmt.Errorf("decode gmail send: %w", err)
	}

	return GmailSendDraftResult{
		MessageID: sendResp.ID,
		ThreadID:  sendResp.ThreadID,
	}, nil
}

// doGet performs an authenticated GET request with a default 1 MB limit.
func (g *GmailClient) doGet(ctx context.Context, accessToken, reqURL string) ([]byte, error) {
	return g.doGetLimit(ctx, accessToken, reqURL, 1<<20)
}

// doGetLimit performs an authenticated GET request with a configurable body limit.
func (g *GmailClient) doGetLimit(ctx context.Context, accessToken, reqURL string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", "Pincer/0.1 (gmail)")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("gmail API status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return body, nil
}

// doPost performs an authenticated POST request with JSON body.
func (g *GmailClient) doPost(ctx context.Context, accessToken, reqURL string, payload []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Pincer/0.1 (gmail)")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("gmail API status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return body, nil
}

// gmailMessageResponse represents a Gmail API message resource.
type gmailMessageResponse struct {
	ID       string   `json:"id"`
	ThreadID string   `json:"threadId"`
	Snippet  string   `json:"snippet"`
	LabelIDs []string `json:"labelIds"`
	Payload  struct {
		Headers []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"headers"`
		MimeType string `json:"mimeType"`
		Body     struct {
			Data string `json:"data"`
			Size int    `json:"size"`
		} `json:"body"`
		Parts []gmailMessagePart `json:"parts"`
	} `json:"payload"`
}

type gmailMessagePart struct {
	MimeType string `json:"mimeType"`
	Filename string `json:"filename"`
	Body     struct {
		AttachmentId string `json:"attachmentId"`
		Data         string `json:"data"`
		Size         int    `json:"size"`
	} `json:"body"`
	Parts []gmailMessagePart `json:"parts"`
}

func (m *gmailMessageResponse) headerValue(name string) string {
	for _, h := range m.Payload.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// extractTextBody extracts the text/plain body from a Gmail message.
// Falls back to snippet if no text part is found.
func (m *gmailMessageResponse) extractTextBody() string {
	// Try top-level body first (simple messages).
	if m.Payload.MimeType == "text/plain" && m.Payload.Body.Data != "" {
		if decoded, err := base64URLDecode(m.Payload.Body.Data); err == nil {
			return string(decoded)
		}
	}

	// Search parts recursively.
	if text := extractTextFromParts(m.Payload.Parts); text != "" {
		return text
	}

	// Fallback to snippet.
	return m.Snippet
}

func extractTextFromParts(parts []gmailMessagePart) string {
	for _, part := range parts {
		if part.MimeType == "text/plain" && part.Body.Data != "" {
			if decoded, err := base64URLDecode(part.Body.Data); err == nil {
				return string(decoded)
			}
		}
		if text := extractTextFromParts(part.Parts); text != "" {
			return text
		}
	}
	return ""
}

// extractAttachments recursively collects attachment metadata from a message.
func (m *gmailMessageResponse) extractAttachments(messageID string) []AttachmentMeta {
	var out []AttachmentMeta
	collectAttachments(m.Payload.Parts, messageID, &out)
	return out
}

func collectAttachments(parts []gmailMessagePart, messageID string, out *[]AttachmentMeta) {
	for _, part := range parts {
		if part.Body.AttachmentId != "" && part.Filename != "" {
			*out = append(*out, AttachmentMeta{
				AttachmentID: part.Body.AttachmentId,
				MessageID:    messageID,
				Filename:     part.Filename,
				MimeType:     part.MimeType,
				Size:         part.Body.Size,
			})
		}
		collectAttachments(part.Parts, messageID, out)
	}
}

// GmailGetAttachmentArgs are the arguments for fetching a Gmail attachment.
type GmailGetAttachmentArgs struct {
	MessageID    string `json:"message_id"`
	AttachmentID string `json:"attachment_id"`
}

// GetAttachment fetches the raw bytes of a Gmail attachment.
func (g *GmailClient) GetAttachment(ctx context.Context, accessToken string, args GmailGetAttachmentArgs) ([]byte, error) {
	if args.MessageID == "" {
		return nil, fmt.Errorf("message_id is required")
	}
	if args.AttachmentID == "" {
		return nil, fmt.Errorf("attachment_id is required")
	}

	attURL := fmt.Sprintf("%s/users/me/messages/%s/attachments/%s",
		gmailAPIBase, url.PathEscape(args.MessageID), url.PathEscape(args.AttachmentID))

	body, err := g.doGetLimit(ctx, accessToken, attURL, maxGmailAttachmentBytes)
	if err != nil {
		return nil, fmt.Errorf("gmail get attachment: %w", err)
	}

	var resp struct {
		Size int    `json:"size"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode gmail attachment: %w", err)
	}

	decoded, err := base64URLDecode(resp.Data)
	if err != nil {
		return nil, fmt.Errorf("decode attachment data: %w", err)
	}

	return decoded, nil
}

// base64URLEncode encodes bytes to URL-safe base64 without padding.
func base64URLEncode(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

// base64URLDecode decodes URL-safe base64 (with or without padding).
func base64URLDecode(s string) ([]byte, error) {
	// Try without padding first (Gmail usually omits padding).
	decoded, err := base64.RawURLEncoding.DecodeString(s)
	if err == nil {
		return decoded, nil
	}
	// Fall back to padded URL-safe encoding.
	return base64.URLEncoding.DecodeString(s)
}
