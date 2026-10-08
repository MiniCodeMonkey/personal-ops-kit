// Package md renders job digests and memos to HTML fragments.
//
// Rendering is goldmark (CommonMark + GFM tables, strikethrough, autolinks) --
// the platform's one dependency. Memos are written by a model, so the slice of
// Markdown they use keeps drifting, and a real parser is cheaper than chasing it.
//
// What the wrapper adds on top of goldmark:
//
//   - Headings shift down one level (a digest's h1 sits under the page's own
//     heading).
//   - Raw HTML in the source is escaped and shown as text, never emitted. The
//     input comes from a model reading untrusted pages, so a crafted title must
//     not be able to inject markup into the dashboard.
//   - Only http(s) URLs become live links or images; goldmark already refuses
//     javascript:/data:/vbscript: destinations, and the tests pin that.
//   - Images load lazily; links carry rel="noreferrer".
//   - LinkIssues turns bare issue keys (ENG-123) into links for jobs that ask
//     for it (ISSUE_LINKS in job.env).
package md

import (
	"bytes"
	"html"
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

var engine = goldmark.New(
	goldmark.WithExtensions(extension.Table, extension.Strikethrough, extension.Linkify),
	goldmark.WithParserOptions(parser.WithASTTransformers(util.Prioritized(shiftHeadings{}, 100))),
	goldmark.WithRendererOptions(renderer.WithNodeRenderers(util.Prioritized(escapeRawHTML{}, 100))),
)

// ToHTML converts a digest to an HTML fragment.
func ToHTML(source string) string {
	var buf bytes.Buffer
	if err := engine.Convert([]byte(strings.ReplaceAll(source, "\r\n", "\n")), &buf); err != nil {
		// goldmark does not fail on malformed Markdown; this is a programming
		// error, and showing the source as text is the honest fallback.
		return "<pre>" + html.EscapeString(source) + "</pre>"
	}
	out := buf.String()
	out = strings.ReplaceAll(out, "<img src=", `<img loading="lazy" src=`)
	out = strings.ReplaceAll(out, "<a href=", `<a rel="noreferrer" href=`)
	return out
}

// shiftHeadings moves every heading down one level (h1 -> h2, ..., h5 -> h6).
type shiftHeadings struct{}

func (shiftHeadings) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if h, ok := n.(*ast.Heading); ok && entering && h.Level < 6 {
			h.Level++
		}
		return ast.WalkContinue, nil
	})
}

// escapeRawHTML renders inline and block HTML from the source as escaped
// text. goldmark's default is to omit it with a comment, which hides the
// content; a model-written digest that mentions <script> or a size like <30cm
// should show exactly that, as text.
type escapeRawHTML struct{}

func (r escapeRawHTML) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindRawHTML, r.renderRawHTML)
	reg.Register(ast.KindHTMLBlock, r.renderHTMLBlock)
}

func (escapeRawHTML) renderRawHTML(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.RawHTML)
	for i := 0; i < n.Segments.Len(); i++ {
		seg := n.Segments.At(i)
		_, _ = w.WriteString(html.EscapeString(string(seg.Value(source))))
	}
	return ast.WalkSkipChildren, nil
}

func (escapeRawHTML) renderHTMLBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.HTMLBlock)
	if entering {
		_, _ = w.WriteString("<p>")
		lines := n.Lines()
		for i := 0; i < lines.Len(); i++ {
			line := lines.At(i)
			_, _ = w.WriteString(html.EscapeString(string(line.Value(source))))
		}
		if n.HasClosure() {
			_, _ = w.WriteString(html.EscapeString(string(n.ClosureLine.Value(source))))
		}
	} else {
		_, _ = w.WriteString("</p>\n")
	}
	return ast.WalkContinue, nil
}

// LinkIssues turns bare issue identifiers in an HTML fragment into links.
// links maps a team key ("ENG") to a URL prefix the identifier is appended to
// ("https://linear.app/acme/issue/"). Text already inside <a>, <code> or <pre>
// is left alone, and only the listed keys match, so ISO-8601 stays text.
//
// Each link carries class="issue" and data-issue so the dashboard can attach a
// hover card fed by the job's state/issues.json.
func LinkIssues(fragment string, links map[string]string) string {
	if len(links) == 0 {
		return fragment
	}
	keys := make([]string, 0, len(links))
	for k := range links {
		keys = append(keys, regexp.QuoteMeta(k))
	}
	sort.Strings(keys)
	pattern := regexp.MustCompile(`\b(` + strings.Join(keys, "|") + `)-([0-9]+)\b`)

	var out strings.Builder
	depth := 0 // nesting inside <a>, <code>, <pre>
	i := 0
	for i < len(fragment) {
		if fragment[i] == '<' {
			j := strings.IndexByte(fragment[i:], '>')
			if j < 0 {
				out.WriteString(fragment[i:])
				break
			}
			tag := fragment[i : i+j+1]
			lower := strings.ToLower(tag)
			switch {
			case strings.HasPrefix(lower, "<a ") || lower == "<a>" || strings.HasPrefix(lower, "<code") || strings.HasPrefix(lower, "<pre"):
				depth++
			case strings.HasPrefix(lower, "</a") || strings.HasPrefix(lower, "</code") || strings.HasPrefix(lower, "</pre"):
				if depth > 0 {
					depth--
				}
			}
			out.WriteString(tag)
			i += j + 1
			continue
		}
		j := strings.IndexByte(fragment[i:], '<')
		if j < 0 {
			j = len(fragment) - i
		}
		txt := fragment[i : i+j]
		if depth == 0 {
			txt = pattern.ReplaceAllStringFunc(txt, func(m string) string {
				key := m[:strings.IndexByte(m, '-')]
				href := html.EscapeString(links[key] + m)
				return `<a class="issue" data-issue="` + m + `" href="` + href + `" rel="noreferrer">` + m + `</a>`
			})
		}
		out.WriteString(txt)
		i += j
	}
	return out.String()
}
