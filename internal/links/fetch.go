package links

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

const maxContentBytes = 2 << 20

var (
	errExternalDisabled = errors.New("external links disabled (pass -external)")
	errNoBaseDir        = errors.New("no base directory for relative links")
	errNotText          = errors.New("content is not text")
	errTooLarge         = errors.New("content exceeds 2 MiB")
	errBadStatus        = errors.New("unexpected HTTP status")
)

// Fetcher retrieves linked content from disk (relative links) or the web (external links).
type Fetcher struct {
	httpClient    *http.Client
	baseDir       string
	allowExternal bool
	logger        *slog.Logger
}

// NewFetcher returns a Fetcher resolving relative links against baseDir.
// External links are only fetched when allowExternal is true.
func NewFetcher(httpClient *http.Client, baseDir string, allowExternal bool, logger *slog.Logger) *Fetcher {
	return &Fetcher{httpClient: httpClient, baseDir: baseDir, allowExternal: allowExternal, logger: logger}
}

// Fetch returns the readable text behind link, with HTML reduced to its visible text.
func (f *Fetcher) Fetch(ctx context.Context, link Link) (string, error) {
	if link.External {
		return f.fetchURL(ctx, link.Target)
	}
	return f.readFile(link.Target)
}

// readFile treats both relative and root-relative ("/docs/x.md") targets as relative to the base directory.
func (f *Fetcher) readFile(target string) (string, error) {
	if f.baseDir == "" {
		return "", errNoBaseDir
	}
	u, err := url.Parse(target)
	if err != nil {
		return "", fmt.Errorf("parsing %q: %w", target, err)
	}
	path := filepath.Join(f.baseDir, filepath.FromSlash(u.Path))
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	if info.IsDir() {
		path = filepath.Join(path, "README.md")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	defer file.Close()
	data, err := readLimited(file)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	if ext := strings.ToLower(filepath.Ext(path)); ext == ".html" || ext == ".htm" {
		return htmlText(data), nil
	}
	return plain(data)
}

func (f *Fetcher) fetchURL(ctx context.Context, target string) (string, error) {
	if !f.allowExternal {
		return "", errExternalDisabled
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL(target), nil)
	if err != nil {
		return "", fmt.Errorf("building request for %s: %w", target, err)
	}
	req.Header.Set("User-Agent", "readtime")
	req.Header.Set("Accept", "text/markdown, text/plain, text/html;q=0.9, */*;q=0.1")
	resp, err := f.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", target, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching %s: %w %d", target, errBadStatus, resp.StatusCode)
	}
	data, err := readLimited(resp.Body)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", target, err)
	}
	f.logger.DebugContext(ctx, "fetched link", "target", target, "bytes", len(data))
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		return htmlText(data), nil
	}
	if mediaType != "" && !strings.HasPrefix(mediaType, "text/") && mediaType != "application/json" {
		return "", fmt.Errorf("fetching %s: %w (%s)", target, errNotText, mediaType)
	}
	return plain(data)
}

// rawURL points GitHub file links at the raw file instead of the surrounding page.
func rawURL(target string) string {
	u, err := url.Parse(target)
	if err != nil || u.Host != "github.com" {
		return target
	}
	parts := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 4)
	if len(parts) < 4 || parts[2] != "blob" {
		return target
	}
	return "https://raw.githubusercontent.com/" + parts[0] + "/" + parts[1] + "/" + parts[3]
}

func readLimited(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxContentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxContentBytes {
		return nil, errTooLarge
	}
	return data, nil
}

func plain(data []byte) (string, error) {
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", errNotText
	}
	return string(data), nil
}

// htmlText keeps visible text, separating block elements into paragraphs.
func htmlText(data []byte) string {
	z := html.NewTokenizer(bytes.NewReader(data))
	var b strings.Builder
	hidden := 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			return strings.TrimSpace(b.String())
		case html.StartTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "head", "script", "style", "noscript", "nav", "header", "footer", "svg", "template":
				hidden++
			case "p", "div", "li", "br", "tr", "section", "article", "h1", "h2", "h3", "h4", "h5", "h6", "pre", "blockquote":
				b.WriteString("\n\n")
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "head", "script", "style", "noscript", "nav", "header", "footer", "svg", "template":
				hidden = max(hidden-1, 0)
			}
		case html.TextToken:
			if hidden == 0 {
				if words := strings.Fields(string(z.Text())); len(words) > 0 {
					b.WriteString(strings.Join(words, " "))
					b.WriteByte(' ')
				}
			}
		}
	}
}
