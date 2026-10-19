package links

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestExtract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		md   string
		want []Link
	}{
		{name: "inline relative link", md: "See [setup](docs/setup.md).", want: []Link{{Text: "setup", Target: "docs/setup.md"}}},
		{name: "reference link", md: "Read [the spec][s].\n\n[s]: https://example.com/spec", want: []Link{{Text: "the spec", Target: "https://example.com/spec", External: true}}},
		{name: "angle autolink", md: "<https://example.com>", want: []Link{{Text: "https://example.com", Target: "https://example.com", External: true}}},
		{name: "bare url", md: "Go to https://example.com/a now", want: []Link{{Text: "https://example.com/a", Target: "https://example.com/a", External: true}}},
		{name: "images anchors and mailto skipped", md: "![x](img.png) [top](#top) [me](mailto:a@b.c)", want: nil},
		{name: "links in code ignored", md: "```\n[x](y.md)\n```\n`[a](b.md)`", want: nil},
		{name: "duplicates collapsed", md: "[a](x.md) and [b](x.md)", want: []Link{{Text: "a", Target: "x.md"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, Extract(tt.md))
		})
	}
}

func TestFetchReadsRelativeFileIgnoringFragment(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "guide.md"), []byte("# Guide"), 0o600))
	got, err := NewFetcher(http.DefaultClient, dir, false, testLogger()).Fetch(t.Context(), Link{Target: "guide.md#install"})
	require.NoError(t, err)
	assert.Equal(t, "# Guide", got)
}

func TestFetchReadsDirectoryReadme(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "docs"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docs", "README.md"), []byte("docs home"), 0o600))
	got, err := NewFetcher(http.DefaultClient, dir, false, testLogger()).Fetch(t.Context(), Link{Target: "docs/"})
	require.NoError(t, err)
	assert.Equal(t, "docs home", got)
}

func TestFetchRejectsBinaryFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0, 1, 2}, 0o600))
	_, err := NewFetcher(http.DefaultClient, dir, false, testLogger()).Fetch(t.Context(), Link{Target: "blob.bin"})
	assert.ErrorIs(t, err, errNotText)
}

func TestFetchSkipsExternalWhenDisabled(t *testing.T) {
	t.Parallel()
	_, err := NewFetcher(http.DefaultClient, "", false, testLogger()).Fetch(t.Context(), Link{Target: "https://example.com", External: true})
	assert.ErrorIs(t, err, errExternalDisabled)
}

func TestFetchExtractsVisibleTextFromHTML(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html><head><title>T</title></head><body><nav>Menu</nav><p>Hello  world</p><script>x()</script><p>Bye</p></body></html>")
	}))
	t.Cleanup(server.Close)
	got, err := NewFetcher(server.Client(), "", true, testLogger()).Fetch(t.Context(), Link{Target: server.URL, External: true})
	require.NoError(t, err)
	assert.Equal(t, "Hello world \n\nBye", got)
}

func TestFetchRejectsNonTextMedia(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = io.WriteString(w, "%PDF")
	}))
	t.Cleanup(server.Close)
	_, err := NewFetcher(server.Client(), "", true, testLogger()).Fetch(t.Context(), Link{Target: server.URL, External: true})
	assert.ErrorIs(t, err, errNotText)
}

func TestFetchReportsHTTPErrors(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	_, err := NewFetcher(server.Client(), "", true, testLogger()).Fetch(t.Context(), Link{Target: server.URL, External: true})
	assert.True(t, errors.Is(err, errBadStatus) && strings.Contains(err.Error(), "404"))
}

func TestRawURLRewritesGitHubBlobLinks(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "https://raw.githubusercontent.com/o/r/main/docs/a.md", rawURL("https://github.com/o/r/blob/main/docs/a.md"))
}

func TestRawURLLeavesOtherLinksAlone(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "https://github.com/o/r/issues/1", rawURL("https://github.com/o/r/issues/1"))
}
