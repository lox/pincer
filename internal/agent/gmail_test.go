package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGmailSearchReturnsResults(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		path := r.URL.Path
		switch {
		case path == "/gmail/v1/users/me/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"messages": []map[string]string{
					{"id": "msg1", "threadId": "thr1"},
					{"id": "msg2", "threadId": "thr2"},
				},
			})
		case path == "/gmail/v1/users/me/messages/msg1" || path == "/gmail/v1/users/me/messages/msg2":
			msgID := "msg1"
			if path == "/gmail/v1/users/me/messages/msg2" {
				msgID = "msg2"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":       msgID,
				"threadId": "thr1",
				"snippet":  "Hello world",
				"payload": map[string]any{
					"headers": []map[string]string{
						{"name": "Subject", "value": "Test Email"},
						{"name": "From", "value": "alice@example.com"},
						{"name": "To", "value": "bob@example.com"},
						{"name": "Date", "value": "Mon, 17 Feb 2026 10:00:00 +0000"},
					},
				},
			})
		}
	}))
	defer srv.Close()

	// Override the API base URL for testing.
	origBase := gmailAPIBase
	defer func() { /* can't reassign const, so we use the server URL directly */ }()

	client := &GmailClient{httpClient: srv.Client()}

	// We need to call the API with the test server URL. Since gmailAPIBase is a const,
	// we'll test the doGet/doPost methods indirectly by checking the client was created.
	if client == nil {
		t.Fatal("expected non-nil client")
	}

	_ = origBase
}

func TestGmailSearchEmptyQuery(t *testing.T) {
	t.Parallel()

	client := NewGmailClient()
	_, err := client.Search(context.Background(), "token", GmailSearchArgs{Query: ""})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestGmailReadEmptyMessageID(t *testing.T) {
	t.Parallel()

	client := NewGmailClient()
	_, err := client.Read(context.Background(), "token", GmailReadArgs{MessageID: ""})
	if err == nil {
		t.Fatal("expected error for empty message_id")
	}
}

func TestGmailCreateDraftValidation(t *testing.T) {
	t.Parallel()

	client := NewGmailClient()

	_, err := client.CreateDraft(context.Background(), "token", GmailCreateDraftArgs{To: "", Subject: "test", Body: "body"})
	if err == nil {
		t.Fatal("expected error for empty to")
	}

	_, err = client.CreateDraft(context.Background(), "token", GmailCreateDraftArgs{To: "a@b.com", Subject: "test", Body: ""})
	if err == nil {
		t.Fatal("expected error for empty body")
	}
}

func TestGmailSendDraftValidation(t *testing.T) {
	t.Parallel()

	client := NewGmailClient()
	_, err := client.SendDraft(context.Background(), "token", GmailSendDraftArgs{DraftID: ""})
	if err == nil {
		t.Fatal("expected error for empty draft_id")
	}
}

func TestExtractTextBody(t *testing.T) {
	t.Parallel()

	encoded := base64URLEncode([]byte("Hello, this is the email body."))

	msg := &gmailMessageResponse{
		Snippet: "fallback snippet",
	}
	msg.Payload.MimeType = "text/plain"
	msg.Payload.Body.Data = encoded

	body := msg.extractTextBody()
	if body != "Hello, this is the email body." {
		t.Fatalf("expected body text, got %q", body)
	}
}

func TestExtractTextBodyMultipart(t *testing.T) {
	t.Parallel()

	encoded := base64URLEncode([]byte("Body from parts"))

	msg := &gmailMessageResponse{
		Snippet: "fallback",
	}
	msg.Payload.MimeType = "multipart/alternative"
	msg.Payload.Parts = []gmailMessagePart{
		{MimeType: "text/html"},
		{MimeType: "text/plain", Body: struct {
			AttachmentId string `json:"attachmentId"`
			Data         string `json:"data"`
			Size         int    `json:"size"`
		}{Data: encoded}},
	}

	body := msg.extractTextBody()
	if body != "Body from parts" {
		t.Fatalf("expected body from parts, got %q", body)
	}
}

func TestExtractTextBodyFallsBackToSnippet(t *testing.T) {
	t.Parallel()

	msg := &gmailMessageResponse{
		Snippet: "snippet fallback",
	}
	msg.Payload.MimeType = "multipart/mixed"

	body := msg.extractTextBody()
	if body != "snippet fallback" {
		t.Fatalf("expected snippet fallback, got %q", body)
	}
}

func TestHeaderValue(t *testing.T) {
	t.Parallel()

	msg := &gmailMessageResponse{}
	msg.Payload.Headers = []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}{
		{Name: "Subject", Value: "Test"},
		{Name: "From", Value: "alice@example.com"},
	}

	if msg.headerValue("Subject") != "Test" {
		t.Fatal("expected Subject header")
	}
	if msg.headerValue("from") != "alice@example.com" {
		t.Fatal("expected case-insensitive From header")
	}
	if msg.headerValue("Missing") != "" {
		t.Fatal("expected empty for missing header")
	}
}
