package browser

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	htmlRedirectMaxBytes     = 64 << 10
	htmlRedirectStubMaxBytes = 8 << 10
	htmlRedirectHTTPTimeout  = 3 * time.Second
)

var (
	metaRefreshURLRe = regexp.MustCompile(`(?i)url\s*=\s*['"]?([^'"\s>]+)`)
	metaRefreshTagRe = regexp.MustCompile(`(?is)<meta[^>]*http-equiv\s*=\s*['"]?refresh['"]?[^>]*>`)
)

// maybeResolveHTMLRedirect fetches the URL over HTTP and, when the document is a
// tiny meta-refresh stub (empty body + content="N;url=..."), returns the absolute
// target. This avoids Lightpanda CDP disconnects on JS/meta client redirects.
func maybeResolveHTMLRedirect(ctx context.Context, rawURL string) (string, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}

	reqCtx, cancel := context.WithTimeout(ctx, htmlRedirectHTTPTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", "AgentMaurice-BrowserMCP/1.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.8")

	client := &http.Client{
		Timeout: htmlRedirectHTTPTimeout,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return "", false
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if contentType != "" && !strings.Contains(contentType, "text/html") &&
		!strings.Contains(contentType, "application/xhtml") {
		return "", false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, htmlRedirectMaxBytes))
	if err != nil || len(body) == 0 || len(body) > htmlRedirectStubMaxBytes {
		return "", false
	}

	target := parseMetaRefreshTarget(string(body))
	if target == "" {
		return "", false
	}

	base := resp.Request.URL
	if base == nil {
		base = parsed
	}
	resolved, err := base.Parse(target)
	if err != nil || resolved.String() == "" {
		return "", false
	}
	if resolved.String() == rawURL || resolved.String() == base.String() {
		return "", false
	}
	return resolved.String(), true
}

// parseMetaRefreshTarget extracts the URL from a meta refresh tag content.
func parseMetaRefreshTarget(html string) string {
	tag := metaRefreshTagRe.FindString(html)
	if tag == "" {
		return ""
	}
	content := metaRefreshContent(tag)
	if content == "" {
		return ""
	}
	urlMatch := metaRefreshURLRe.FindStringSubmatch(content)
	if len(urlMatch) < 2 {
		return ""
	}
	return strings.TrimSpace(urlMatch[1])
}

func metaRefreshContent(tag string) string {
	lower := strings.ToLower(tag)
	idx := strings.Index(lower, "content=")
	if idx < 0 {
		return ""
	}
	rest := tag[idx+len("content="):]
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return ""
	}
	quote := rest[0]
	if quote != '\'' && quote != '"' {
		// Unquoted content ends at whitespace or tag close.
		end := strings.IndexAny(rest, " \t>")
		if end < 0 {
			return rest
		}
		return rest[:end]
	}
	rest = rest[1:]
	end := strings.IndexByte(rest, quote)
	if end < 0 {
		return ""
	}
	return rest[:end]
}
