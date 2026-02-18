package agent

import (
	"testing"
)

func TestImageProxyRewriterBasicImage(t *testing.T) {
	r := NewImageProxyRewriter([]byte("test-key"), "/proxy/image")

	input := `Here is an image: ![cat](https://example.com/cat.jpg)`
	result := r.Rewrite(input)

	if result == input {
		t.Fatal("expected image URL to be rewritten")
	}
	if !contains(result, "/proxy/image?url=") {
		t.Fatalf("expected proxied URL, got: %s", result)
	}
	if !contains(result, "&sig=") {
		t.Fatalf("expected HMAC signature, got: %s", result)
	}
	if contains(result, "https://example.com/cat.jpg") {
		t.Fatalf("original URL should be replaced, got: %s", result)
	}
	// Alt text should be preserved.
	if !contains(result, "![cat]") {
		t.Fatalf("alt text should be preserved, got: %s", result)
	}
}

func TestImageProxyRewriterMultipleImages(t *testing.T) {
	r := NewImageProxyRewriter([]byte("test-key"), "/proxy/image")

	input := "![a](https://a.com/1.jpg) and ![b](https://b.com/2.png)"
	result := r.Rewrite(input)

	if contains(result, "https://a.com/1.jpg") {
		t.Fatalf("first URL should be replaced, got: %s", result)
	}
	if contains(result, "https://b.com/2.png") {
		t.Fatalf("second URL should be replaced, got: %s", result)
	}
}

func TestImageProxyRewriterNoImages(t *testing.T) {
	r := NewImageProxyRewriter([]byte("test-key"), "/proxy/image")

	input := "Just some **bold** text and a [link](https://example.com)"
	result := r.Rewrite(input)

	if result != input {
		t.Fatalf("expected no changes, got: %s", result)
	}
}

func TestImageProxyRewriterStripsHTMLImgTag(t *testing.T) {
	r := NewImageProxyRewriter([]byte("test-key"), "/proxy/image")

	input := "Text before\n\n<img src=\"https://evil.com/track.gif\">\n\nText after"
	result := r.Rewrite(input)

	if contains(result, "evil.com") {
		t.Fatalf("HTML img tag should be stripped, got: %s", result)
	}
	if contains(result, "<img") {
		t.Fatalf("HTML img tag should be stripped, got: %s", result)
	}
}

func TestImageProxyRewriterStripsInlineHTML(t *testing.T) {
	r := NewImageProxyRewriter([]byte("test-key"), "/proxy/image")

	input := "Some text with <img src=\"https://evil.com/pixel.gif\"> inline"
	result := r.Rewrite(input)

	if contains(result, "evil.com") {
		t.Fatalf("inline HTML should be stripped, got: %s", result)
	}
}

func TestImageProxyRewriterPreservesRegularMarkdown(t *testing.T) {
	r := NewImageProxyRewriter([]byte("test-key"), "/proxy/image")

	input := "# Heading\n\nSome **bold** and *italic* text.\n\n- list item\n- another item\n\n[link](https://example.com)"
	result := r.Rewrite(input)

	if result != input {
		t.Fatalf("regular markdown should be unchanged, got: %s", result)
	}
}

func TestImageProxyRewriterMixedContent(t *testing.T) {
	r := NewImageProxyRewriter([]byte("test-key"), "/proxy/image")

	input := "Here is a photo:\n\n![photo](https://photos.com/img.jpg)\n\nAnd a [link](https://example.com) that should stay."
	result := r.Rewrite(input)

	if contains(result, "https://photos.com/img.jpg") {
		t.Fatalf("image URL should be replaced, got: %s", result)
	}
	if !contains(result, "https://example.com") {
		t.Fatalf("regular link should be preserved, got: %s", result)
	}
}

func TestVerifyURL(t *testing.T) {
	key := []byte("test-key")
	r := NewImageProxyRewriter(key, "/proxy/image")

	imageURL := "https://example.com/cat.jpg"
	sig := r.SignURL(imageURL)

	if !VerifyURL(key, imageURL, sig) {
		t.Fatal("valid signature should verify")
	}
	if VerifyURL(key, imageURL, "bad-sig") {
		t.Fatal("bad signature should not verify")
	}
	if VerifyURL(key, "https://other.com/evil.jpg", sig) {
		t.Fatal("wrong URL should not verify")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
