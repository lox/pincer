package agent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchArgsValidation(t *testing.T) {
	fetcher := NewWebFetcher()
	_, err := fetcher.Fetch(t.Context(), FetchArgs{URL: ""})
	if err == nil {
		t.Fatal("expected error for empty url")
	}
}

func TestFetchBadScheme(t *testing.T) {
	fetcher := NewWebFetcher()

	for _, u := range []string{"ftp://example.com/file", "file:///etc/passwd", "gopher://example.com"} {
		_, err := fetcher.Fetch(t.Context(), FetchArgs{URL: u})
		if err == nil {
			t.Fatalf("expected error for scheme %q", u)
		}
		if !strings.Contains(err.Error(), "ssrf") {
			t.Fatalf("expected SSRF error for %q, got: %v", u, err)
		}
	}
}

func TestFetchRejectsURLWithCredentials(t *testing.T) {
	fetcher := NewWebFetcher()
	_, err := fetcher.Fetch(t.Context(), FetchArgs{URL: "https://user:pass@example.com/page"})
	if err == nil {
		t.Fatal("expected error for URL with embedded credentials")
	}
	if !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("expected credentials error, got: %v", err)
	}
}

func TestFetchBlocksPrivateIPs(t *testing.T) {
	blocked := []string{
		"http://127.0.0.1/test",
		"http://127.0.0.2:8080/test",
		"http://10.0.0.1/test",
		"http://172.16.0.1/test",
		"http://192.168.1.1/test",
		"http://[::1]/test",
		"http://0.0.0.0/test",
	}

	fetcher := NewWebFetcher()
	for _, u := range blocked {
		_, err := fetcher.Fetch(t.Context(), FetchArgs{URL: u})
		if err == nil {
			t.Fatalf("expected error for blocked IP %q", u)
		}
		if !strings.Contains(err.Error(), "ssrf") && !strings.Contains(err.Error(), "blocked") {
			t.Fatalf("expected SSRF/blocked error for %q, got: %v", u, err)
		}
	}
}

// newTestFetcher creates a fetcher that bypasses SSRF dial checks,
// used for tests against httptest.Server (which binds to 127.0.0.1).
func newTestFetcher() *WebFetcher {
	return NewWebFetcherWithTransport(http.DefaultTransport)
}

func TestFetchHappyPath(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "hello from test server")
	}))
	defer ts.Close()

	fetcher := newTestFetcher()
	result, err := fetcher.Fetch(t.Context(), FetchArgs{URL: ts.URL + "/test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StatusCode != 200 {
		t.Fatalf("expected status 200, got %d", result.StatusCode)
	}
	if result.Body != "hello from test server" {
		t.Fatalf("unexpected body: %q", result.Body)
	}
	if result.Truncated {
		t.Fatal("expected truncated=false")
	}
	if !strings.Contains(result.ContentType, "text/plain") {
		t.Fatalf("expected text/plain content type, got %q", result.ContentType)
	}
}

func TestFetchPrefersMarkdownContentNegotiation(t *testing.T) {
	var acceptHeader string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptHeader = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		fmt.Fprint(w, "# Hello\n\nThis is markdown.")
	}))
	defer ts.Close()

	fetcher := newTestFetcher()
	result, err := fetcher.Fetch(t.Context(), FetchArgs{URL: ts.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(acceptHeader, "text/markdown") {
		t.Fatalf("expected Accept header to include text/markdown, got %q", acceptHeader)
	}
	if !strings.Contains(result.ContentType, "text/markdown") {
		t.Fatalf("expected markdown content type, got %q", result.ContentType)
	}
	if !strings.Contains(result.Body, "# Hello") {
		t.Fatalf("expected markdown body, got %q", result.Body)
	}
}

func TestFetchConvertsHTMLToMarkdown(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<h1>Page Title</h1>
			<p>This is a <strong>bold</strong> paragraph with a <a href="https://example.com">link</a>.</p>
			<ul><li>Item one</li><li>Item two</li></ul>
		</body></html>`)
	}))
	defer ts.Close()

	fetcher := newTestFetcher()
	result, err := fetcher.Fetch(t.Context(), FetchArgs{URL: ts.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should contain markdown heading, not raw HTML tags.
	if !strings.Contains(result.Body, "# Page Title") {
		t.Fatalf("expected markdown heading, got:\n%s", result.Body)
	}
	if !strings.Contains(result.Body, "**bold**") {
		t.Fatalf("expected bold markdown, got:\n%s", result.Body)
	}
	if !strings.Contains(result.Body, "[link](https://example.com)") {
		t.Fatalf("expected markdown link, got:\n%s", result.Body)
	}
	// Should not contain raw HTML tags.
	if strings.Contains(result.Body, "<h1>") || strings.Contains(result.Body, "<strong>") {
		t.Fatalf("expected HTML tags to be converted, got:\n%s", result.Body)
	}
}

func TestFetchReturnsSummaryForBinaryContent(t *testing.T) {
	for _, ct := range []string{"image/png", "image/jpeg", "audio/mpeg", "video/mp4", "application/octet-stream", "application/pdf"} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", ct)
			w.Write([]byte{0x89, 0x50, 0x4e, 0x47}) // binary junk
		}))

		fetcher := newTestFetcher()
		result, err := fetcher.Fetch(t.Context(), FetchArgs{URL: ts.URL})
		ts.Close()
		if err != nil {
			t.Fatalf("[%s] unexpected error: %v", ct, err)
		}
		if !strings.Contains(result.Body, "Binary content") {
			t.Fatalf("[%s] expected binary content message, got: %q", ct, result.Body)
		}
		if !strings.Contains(result.Body, "web_summarize") {
			t.Fatalf("[%s] expected web_summarize suggestion, got: %q", ct, result.Body)
		}
		if result.StatusCode != 200 {
			t.Fatalf("[%s] expected status 200, got %d", ct, result.StatusCode)
		}
	}
}

func TestFetchSkipsConversionForNonHTML(t *testing.T) {
	jsonBody := `{"key":"value","items":[1,2,3]}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, jsonBody)
	}))
	defer ts.Close()

	fetcher := newTestFetcher()
	result, err := fetcher.Fetch(t.Context(), FetchArgs{URL: ts.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Body != jsonBody {
		t.Fatalf("expected JSON body unchanged, got:\n%s", result.Body)
	}
}

func TestFetchResolvesRelativeLinksInHTML(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><a href="/about">About</a></body></html>`)
	}))
	defer ts.Close()

	fetcher := newTestFetcher()
	result, err := fetcher.Fetch(t.Context(), FetchArgs{URL: ts.URL + "/page"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Relative /about should be resolved to absolute URL.
	if !strings.Contains(result.Body, ts.URL+"/about") {
		t.Fatalf("expected relative link to be resolved to absolute, got:\n%s", result.Body)
	}
}

func TestFetchTracksFinalURL(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		fmt.Fprint(w, "landed")
	}))
	defer ts.Close()

	fetcher := newTestFetcher()
	result, err := fetcher.Fetch(t.Context(), FetchArgs{URL: ts.URL + "/start"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FinalURL == "" {
		t.Fatal("expected FinalURL to be set after redirect")
	}
	if !strings.HasSuffix(result.FinalURL, "/final") {
		t.Fatalf("expected FinalURL to end with /final, got %q", result.FinalURL)
	}
}

func TestFetchTruncatesLargeResponse(t *testing.T) {
	bigBody := strings.Repeat("x", maxFetchResponseBytes+1000)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, bigBody)
	}))
	defer ts.Close()

	fetcher := newTestFetcher()
	result, err := fetcher.Fetch(t.Context(), FetchArgs{URL: ts.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Truncated {
		t.Fatal("expected truncated=true for large response")
	}
	if len(result.Body) != maxFetchResponseBytes {
		t.Fatalf("expected body length %d, got %d", maxFetchResponseBytes, len(result.Body))
	}
}

func TestFetchRedirectLimit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path, http.StatusFound)
	}))
	defer ts.Close()

	fetcher := newTestFetcher()
	_, err := fetcher.Fetch(t.Context(), FetchArgs{URL: ts.URL + "/loop"})
	if err == nil {
		t.Fatal("expected error for redirect loop")
	}
	if !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("expected redirect error, got: %v", err)
	}
}

func TestFetchNon200Status(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "page not found")
	}))
	defer ts.Close()

	fetcher := newTestFetcher()
	result, err := fetcher.Fetch(t.Context(), FetchArgs{URL: ts.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StatusCode != 404 {
		t.Fatalf("expected status 404, got %d", result.StatusCode)
	}
	if result.Body != "page not found" {
		t.Fatalf("unexpected body: %q", result.Body)
	}
}

func TestFetchCanceledContext(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	fetcher := newTestFetcher()
	_, err := fetcher.Fetch(ctx, FetchArgs{URL: ts.URL})
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}

func TestIsBlockedIP(t *testing.T) {
	tests := []struct {
		ip      string
		blocked bool
	}{
		// Loopback
		{"127.0.0.1", true},
		{"127.0.0.2", true},
		{"127.255.255.255", true},
		// Private (RFC 1918)
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.0.1", true},
		{"192.168.255.255", true},
		// "This network" (0.0.0.0/8)
		{"0.0.0.0", true},
		{"0.1.2.3", true},
		// CGNAT (100.64.0.0/10)
		{"100.64.0.1", true},
		{"100.127.255.255", true},
		// Benchmark (198.18.0.0/15)
		{"198.18.0.1", true},
		{"198.19.255.255", true},
		// TEST-NETs
		{"192.0.2.1", true},
		{"198.51.100.1", true},
		{"203.0.113.1", true},
		// Link-local
		{"169.254.1.1", true},
		// IPv6
		{"::1", true},
		{"fe80::1", true},
		{"fc00::1", true},
		{"fd12::1", true},
		{"ff02::1", true},
		// Public IPs should not be blocked.
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"93.184.216.34", false},
		{"100.128.0.1", false}, // just outside CGNAT range
		{"198.20.0.1", false},  // just outside benchmark range
	}

	for _, tt := range tests {
		ip := net.ParseIP(tt.ip)
		if ip == nil {
			t.Fatalf("failed to parse IP %q", tt.ip)
		}
		got := isBlockedIP(ip)
		if got != tt.blocked {
			t.Errorf("isBlockedIP(%s) = %v, want %v", tt.ip, got, tt.blocked)
		}
	}
}
