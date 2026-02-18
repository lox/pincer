package agent

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// ImageProxyRewriter rewrites image URLs in markdown to proxied versions
// and strips raw HTML to prevent exfiltration via untrusted model output.
type ImageProxyRewriter struct {
	hmacKey  []byte
	proxyURL string // e.g. "/proxy/image"
}

// NewImageProxyRewriter creates a rewriter that signs image URLs with HMAC.
func NewImageProxyRewriter(hmacKey []byte, proxyURL string) *ImageProxyRewriter {
	return &ImageProxyRewriter{
		hmacKey:  hmacKey,
		proxyURL: strings.TrimSuffix(proxyURL, "/"),
	}
}

// SignURL generates an HMAC-SHA256 signature for a URL.
func (r *ImageProxyRewriter) SignURL(rawURL string) string {
	mac := hmac.New(sha256.New, r.hmacKey)
	mac.Write([]byte(rawURL))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyURL checks the HMAC signature for a URL.
func VerifyURL(hmacKey []byte, rawURL, sig string) bool {
	mac := hmac.New(sha256.New, hmacKey)
	mac.Write([]byte(rawURL))
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(sig))
}

// ProxiedURL returns the proxied URL with HMAC signature.
func (r *ImageProxyRewriter) ProxiedURL(imageURL string) string {
	sig := r.SignURL(imageURL)
	return fmt.Sprintf("%s?url=%s&sig=%s", r.proxyURL, url.QueryEscape(imageURL), sig)
}

// imageReplacement tracks a byte range in the source to replace.
type imageReplacement struct {
	start int // byte offset of '(' after ![alt]
	end   int // byte offset after closing ')'
	url   string
}

// Rewrite parses markdown, rewrites image URLs to proxied versions,
// and strips raw HTML blocks/inlines.
func (r *ImageProxyRewriter) Rewrite(markdown string) string {
	source := []byte(markdown)
	parser := goldmark.DefaultParser()
	doc := parser.Parse(text.NewReader(source))

	var replacements []imageReplacement
	var htmlRanges []imageReplacement // ranges to strip

	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}

		switch node := n.(type) {
		case *ast.Image:
			dest := string(node.Destination)
			if dest == "" {
				return ast.WalkContinue, nil
			}
			// Find the destination in the source by scanning from the node's
			// text segment end. The markdown syntax is ![alt](destination).
			// We need the byte positions of the destination to replace it.
			destStart, destEnd := findImageDestination(source, node)
			if destStart >= 0 && destEnd > destStart {
				replacements = append(replacements, imageReplacement{
					start: destStart,
					end:   destEnd,
					url:   dest,
				})
			}

		case *ast.HTMLBlock:
			// Strip raw HTML blocks entirely.
			for i := 0; i < node.Lines().Len(); i++ {
				seg := node.Lines().At(i)
				htmlRanges = append(htmlRanges, imageReplacement{
					start: seg.Start,
					end:   seg.Stop,
				})
			}

		case *ast.RawHTML:
			// Strip inline raw HTML.
			for i := 0; i < node.Segments.Len(); i++ {
				seg := node.Segments.At(i)
				htmlRanges = append(htmlRanges, imageReplacement{
					start: seg.Start,
					end:   seg.Stop,
				})
			}
		}

		return ast.WalkContinue, nil
	})

	if len(replacements) == 0 && len(htmlRanges) == 0 {
		return markdown
	}

	// Build result by applying replacements from end to start (so offsets stay valid).
	result := make([]byte, len(source))
	copy(result, source)

	// Merge and sort all replacements by start position descending.
	type replacement struct {
		start       int
		end         int
		replacement string
	}
	var allReplacements []replacement

	for _, r := range htmlRanges {
		allReplacements = append(allReplacements, replacement{
			start:       r.start,
			end:         r.end,
			replacement: "", // strip
		})
	}
	for _, rep := range replacements {
		allReplacements = append(allReplacements, replacement{
			start:       rep.start,
			end:         rep.end,
			replacement: r.ProxiedURL(rep.url),
		})
	}

	// Sort descending by start so we can replace without offset shifting.
	for i := 0; i < len(allReplacements); i++ {
		for j := i + 1; j < len(allReplacements); j++ {
			if allReplacements[j].start > allReplacements[i].start {
				allReplacements[i], allReplacements[j] = allReplacements[j], allReplacements[i]
			}
		}
	}

	for _, rep := range allReplacements {
		before := result[:rep.start]
		after := result[rep.end:]
		result = append(before, append([]byte(rep.replacement), after...)...)
	}

	return string(result)
}

// findImageDestination locates the byte range of the URL inside ![alt](url)
// by walking backward from the image node's position in the source.
func findImageDestination(source []byte, img *ast.Image) (int, int) {
	dest := img.Destination
	if len(dest) == 0 {
		return -1, -1
	}

	// The image node has child text segments for the alt text.
	// The destination URL follows after the `](` sequence.
	// Find the last text segment of the alt text, then scan forward.
	var lastEnd int
	if img.ChildCount() > 0 {
		for c := img.LastChild(); c != nil; c = c.PreviousSibling() {
			if t, ok := c.(*ast.Text); ok {
				seg := t.Segment
				lastEnd = seg.Stop
				break
			}
		}
	}
	if lastEnd == 0 {
		// No text children — try using HasChildren to find position.
		// Fall back to scanning for the destination string.
		return findDestinationByScanning(source, dest)
	}

	// Scan forward from lastEnd to find '](' then the destination.
	for i := lastEnd; i < len(source)-1; i++ {
		if source[i] == ']' && source[i+1] == '(' {
			destStart := i + 2
			destEnd := destStart + len(dest)
			if destEnd <= len(source) && string(source[destStart:destEnd]) == string(dest) {
				return destStart, destEnd
			}
		}
	}

	return findDestinationByScanning(source, dest)
}

// ProxiedImage holds a proxied image's original URL and HMAC signature.
type ProxiedImage struct {
	OriginalURL string
	Sig         string
}

// ExtractProxiedURLs returns all (originalURL, sig) pairs from a rewritten markdown string.
func ExtractProxiedURLs(proxyPrefix, markdown string) []ProxiedImage {
	var results []ProxiedImage
	// Scan for proxyPrefix occurrences and extract url= and sig= query params.
	search := proxyPrefix + "?"
	s := markdown
	for {
		idx := strings.Index(s, search)
		if idx < 0 {
			break
		}
		// Find the closing paren that ends the markdown image URL.
		paramStart := idx + len(search)
		end := strings.IndexByte(s[paramStart:], ')')
		if end < 0 {
			break
		}
		query := s[paramStart : paramStart+end]
		vals, err := url.ParseQuery(query)
		if err == nil && vals.Get("url") != "" && vals.Get("sig") != "" {
			results = append(results, ProxiedImage{
				OriginalURL: vals.Get("url"),
				Sig:         vals.Get("sig"),
			})
		}
		s = s[paramStart+end:]
	}
	return results
}

// findDestinationByScanning is a fallback that searches for ![...](dest)
// patterns in the source.
func findDestinationByScanning(source []byte, dest []byte) (int, int) {
	destStr := string(dest)
	target := "](" + destStr + ")"
	idx := strings.Index(string(source), target)
	if idx < 0 {
		return -1, -1
	}
	start := idx + 2 // skip ](
	return start, start + len(destStr)
}
