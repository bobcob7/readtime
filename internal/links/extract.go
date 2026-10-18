// Package links finds followable links in Markdown and fetches the readable text behind them.
package links

import (
	"net/url"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

// Link is a followable reference found in a document.
type Link struct {
	Text     string
	Target   string
	External bool
}

// Extract returns the unique http(s) and relative-path links in markdown, in document order.
// Images, in-page anchors and other schemes such as mailto are ignored.
func Extract(markdown string) []Link {
	source := []byte(markdown)
	doc := goldmark.New(goldmark.WithExtensions(extension.Linkify)).Parser().Parse(text.NewReader(source))
	var found []Link
	seen := make(map[string]bool)
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		var link Link
		switch node := n.(type) {
		case *ast.Image:
			return ast.WalkSkipChildren, nil
		case *ast.Link:
			link = Link{Text: plainText(node, source), Target: string(node.Destination)}
		case *ast.AutoLink:
			if node.AutoLinkType != ast.AutoLinkURL {
				return ast.WalkContinue, nil
			}
			target := string(node.URL(source))
			link = Link{Text: target, Target: target}
		default:
			return ast.WalkContinue, nil
		}
		external, ok := classify(link.Target)
		if !ok || seen[link.Target] {
			return ast.WalkSkipChildren, nil
		}
		seen[link.Target] = true
		link.External = external
		found = append(found, link)
		return ast.WalkSkipChildren, nil
	})
	return found
}

// classify reports whether target is external and whether it can be followed at all.
func classify(target string) (bool, bool) {
	u, err := url.Parse(strings.TrimSpace(target))
	if err != nil {
		return false, false
	}
	switch u.Scheme {
	case "http", "https":
		return true, u.Host != ""
	case "":
		return false, u.Path != ""
	default:
		return false, false
	}
}

func plainText(n ast.Node, source []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if t, ok := c.(*ast.Text); ok && entering {
			b.Write(t.Segment.Value(source))
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}
