package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
)

const (
	defaultFetchTimeout    = 30 * time.Second
	maxFetchResponseBytes  = 1024 * 1024 // 1 MB
	maxFetchRedirects      = 5
	maxFetchContentTypeLen = 512
)

var (
	ErrSSRFBlocked    = errors.New("ssrf: request blocked")
	ErrFetchBadScheme = fmt.Errorf("%w: only http and https schemes are allowed", ErrSSRFBlocked)
	ErrFetchUserinfo  = fmt.Errorf("%w: urls with embedded credentials are not allowed", ErrSSRFBlocked)
)

// blockedPrefixes is the canonical list of CIDR ranges that web_fetch must
// never connect to.  Any resolved IP that falls in one of these is rejected.
var blockedPrefixes = []netip.Prefix{
	// IPv4 reserved/private/special-use
	netip.MustParsePrefix("0.0.0.0/8"),         // "this network" (RFC 791)
	netip.MustParsePrefix("10.0.0.0/8"),         // private (RFC 1918)
	netip.MustParsePrefix("100.64.0.0/10"),      // CGNAT / shared address space (RFC 6598)
	netip.MustParsePrefix("127.0.0.0/8"),        // loopback (RFC 1122)
	netip.MustParsePrefix("169.254.0.0/16"),     // link-local (RFC 3927)
	netip.MustParsePrefix("172.16.0.0/12"),      // private (RFC 1918)
	netip.MustParsePrefix("192.0.0.0/24"),       // IETF protocol assignments (RFC 6890)
	netip.MustParsePrefix("192.0.2.0/24"),       // TEST-NET-1 (RFC 5737)
	netip.MustParsePrefix("192.168.0.0/16"),     // private (RFC 1918)
	netip.MustParsePrefix("198.18.0.0/15"),      // benchmark testing (RFC 2544)
	netip.MustParsePrefix("198.51.100.0/24"),    // TEST-NET-2 (RFC 5737)
	netip.MustParsePrefix("203.0.113.0/24"),     // TEST-NET-3 (RFC 5737)
	netip.MustParsePrefix("224.0.0.0/4"),        // multicast (RFC 5771)
	netip.MustParsePrefix("240.0.0.0/4"),        // reserved (RFC 1112)
	netip.MustParsePrefix("255.255.255.255/32"), // limited broadcast

	// IPv6 reserved/private/special-use
	netip.MustParsePrefix("::1/128"),       // loopback
	netip.MustParsePrefix("::/128"),        // unspecified
	netip.MustParsePrefix("fc00::/7"),      // unique local (RFC 4193)
	netip.MustParsePrefix("fe80::/10"),     // link-local
	netip.MustParsePrefix("ff00::/8"),      // multicast
	netip.MustParsePrefix("::ffff:0:0/96"), // IPv4-mapped IPv6 (checked via inner v4)
}

// FetchArgs are the arguments for the web_fetch tool call.
type FetchArgs struct {
	URL string `json:"url"`
}

// FetchResult is the result of a raw URL fetch.
type FetchResult struct {
	URL         string `json:"url"`
	FinalURL    string `json:"final_url,omitempty"`
	StatusCode  int    `json:"status_code"`
	ContentType string `json:"content_type"`
	Body        string `json:"body"`
	Truncated   bool   `json:"truncated"`
}

// WebFetcher fetches raw URL content with SSRF protections.
type WebFetcher struct {
	httpClient *http.Client
}

// NewWebFetcher creates a WebFetcher with SSRF-safe transport.
func NewWebFetcher() *WebFetcher {
	return NewWebFetcherWithTransport(nil)
}

// NewWebFetcherWithTransport creates a WebFetcher with a custom transport.
// Pass nil to use the default SSRF-safe transport.
func NewWebFetcherWithTransport(transport http.RoundTripper) *WebFetcher {
	if transport == nil {
		base := http.DefaultTransport.(*http.Transport).Clone()
		base.DialContext = ssrfSafeDialContext
		base.Proxy = nil // ignore env proxies; defense-in-depth
		transport = base
	}

	client := &http.Client{
		Timeout:   defaultFetchTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxFetchRedirects {
				return fmt.Errorf("stopped after %d redirects", maxFetchRedirects)
			}
			if err := validateFetchURL(req.URL); err != nil {
				return err
			}
			return nil
		},
	}

	return &WebFetcher{httpClient: client}
}

// Fetch retrieves the content at the given URL.
func (f *WebFetcher) Fetch(ctx context.Context, args FetchArgs) (FetchResult, error) {
	targetURL := strings.TrimSpace(args.URL)
	if targetURL == "" {
		return FetchResult{}, fmt.Errorf("url is required")
	}

	parsed, err := url.Parse(targetURL)
	if err != nil {
		return FetchResult{}, fmt.Errorf("invalid url: %w", err)
	}
	if err := validateFetchURL(parsed); err != nil {
		return FetchResult{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return FetchResult{}, err
	}
	req.Header.Set("User-Agent", "Pincer/0.1 (web_fetch)")
	// Prefer markdown (Cloudflare Markdown for Agents), fall back to HTML/JSON/text.
	req.Header.Set("Accept", "text/markdown, text/html;q=0.9, application/json;q=0.8, text/plain;q=0.7, */*;q=0.5")

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return FetchResult{}, fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	if len(contentType) > maxFetchContentTypeLen {
		contentType = contentType[:maxFetchContentTypeLen]
	}

	if isBinaryContentType(contentType) {
		finalURL := ""
		if resp.Request != nil && resp.Request.URL != nil {
			if final := resp.Request.URL.String(); final != targetURL {
				finalURL = final
			}
		}
		return FetchResult{
			URL:         targetURL,
			FinalURL:    finalURL,
			StatusCode:  resp.StatusCode,
			ContentType: contentType,
			Body:        fmt.Sprintf("Binary content (%s). Use web_summarize to analyze this URL.", contentType),
		}, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchResponseBytes+1))
	if err != nil {
		return FetchResult{}, fmt.Errorf("read response body: %w", err)
	}

	truncated := false
	if len(body) > maxFetchResponseBytes {
		body = body[:maxFetchResponseBytes]
		truncated = true
	}

	finalURL := ""
	if resp.Request != nil && resp.Request.URL != nil {
		final := resp.Request.URL.String()
		if final != targetURL {
			finalURL = final
		}
	}

	// If response is HTML (and not already markdown from CF), convert to markdown.
	bodyStr := string(body)
	if isHTMLContentType(contentType) {
		if md, err := htmlToMarkdown(bodyStr, parsed); err == nil && strings.TrimSpace(md) != "" {
			bodyStr = md
		}
	}

	return FetchResult{
		URL:         targetURL,
		FinalURL:    finalURL,
		StatusCode:  resp.StatusCode,
		ContentType: contentType,
		Body:        bodyStr,
		Truncated:   truncated,
	}, nil
}

// isBinaryContentType returns true if the content-type indicates non-text content
// (images, audio, video, etc.) that should not be read as a string.
func isBinaryContentType(ct string) bool {
	ct = strings.ToLower(ct)
	for _, prefix := range []string{"image/", "audio/", "video/", "application/octet-stream", "application/pdf", "application/zip"} {
		if strings.Contains(ct, prefix) {
			return true
		}
	}
	return false
}

// isHTMLContentType returns true if the content-type indicates HTML.
func isHTMLContentType(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml")
}

// htmlToMarkdown converts an HTML string to markdown using html-to-markdown.
// The pageURL is used to resolve relative links/images to absolute URLs.
func htmlToMarkdown(html string, pageURL *url.URL) (string, error) {
	opts := []converter.ConvertOptionFunc{}
	if pageURL != nil {
		domain := pageURL.Scheme + "://" + pageURL.Host
		opts = append(opts, converter.WithDomain(domain))
	}
	return htmltomarkdown.ConvertString(html, opts...)
}

func validateFetchURL(u *url.URL) error {
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return ErrFetchBadScheme
	}
	if u.User != nil {
		return ErrFetchUserinfo
	}
	return nil
}

// ssrfSafeDialContext wraps the default dialer to block connections to
// private, loopback, and link-local addresses.
func ssrfSafeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSSRFBlocked, err)
	}

	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("dns lookup failed: %w", err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("dns lookup returned no addresses for %s", host)
	}

	for _, ip := range ips {
		if isBlockedIP(ip.IP) {
			return nil, fmt.Errorf("%w: %s resolves to blocked address %s", ErrSSRFBlocked, host, ip.IP)
		}
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}

// isBlockedIP returns true if the IP falls within any blocked CIDR prefix.
func isBlockedIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true // unparseable → block
	}
	addr = addr.Unmap() // normalize IPv4-mapped IPv6 to IPv4

	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
