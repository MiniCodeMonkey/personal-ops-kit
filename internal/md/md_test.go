package md

import (
	"strings"
	"testing"
)

func TestBlocks(t *testing.T) {
	cases := []struct{ in, want string }{
		{"## Halloween", "<h3>Halloween</h3>"},
		{"# Best find", "<h2>Best find</h2>"},
		{"- one\n- two", "<li>one</li>"},
		{"plain text", "<p>plain text"},
		{"---", "<hr>"},
	}
	for _, c := range cases {
		if got := ToHTML(c.in); !strings.Contains(got, c.want) {
			t.Errorf("ToHTML(%q) = %q, want it to contain %q", c.in, got, c.want)
		}
	}
}

func TestInlineMarkup(t *testing.T) {
	cases := []struct{ in, want string }{
		{"**Festool TS55**", "<strong>Festool TS55</strong>"},
		{"a *quiet* day", "<em>quiet</em>"},
		{"the `job.env` file", "<code>job.env</code>"},
		{"[the lot](https://example.com/x)", `href="https://example.com/x"`},
		{"see https://example.com/y for more", `href="https://example.com/y"`},
		{"_Dry run_ only", "<em>Dry run</em>"},
		{"~~gone~~", "<del>gone</del>"},
	}
	for _, c := range cases {
		if got := ToHTML(c.in); !strings.Contains(got, c.want) {
			t.Errorf("ToHTML(%q) = %q, want it to contain %q", c.in, got, c.want)
		}
	}
}

// Digests attach a lot thumbnail under each bullet, usually with an empty alt text.
func TestImages(t *testing.T) {
	cases := []struct{ in, want string }{
		{"![](https://media.example.com/t.jpg)", `<img loading="lazy" src="https://media.example.com/t.jpg" alt="">`},
		{"![the lot](https://media.example.com/t.jpg)", `<img loading="lazy" src="https://media.example.com/t.jpg" alt="the lot">`},
	}
	for _, c := range cases {
		got := ToHTML(c.in)
		if !strings.Contains(got, c.want) {
			t.Errorf("ToHTML(%q) = %q, want it to contain %q", c.in, got, c.want)
		}
		if strings.Contains(got, "![") || strings.Contains(got, "!<a") {
			t.Errorf("image syntax leaked into the output as text or a link: %q", got)
		}
	}
}

// A digest is written by a model reading untrusted web pages, so a prompt-injected
// listing could make it emit a link or image with a scripting scheme. Only http(s)
// may become a live href or src; anything else stays inert text.
func TestScriptingSchemesStayInert(t *testing.T) {
	cases := []string{
		"[click me](javascript:alert(1))",
		"![](javascript:alert(1))",
		"![x](data:text/html,<script>alert(1)</script>)",
		"[x](vbscript:msgbox)",
	}
	for _, in := range cases {
		got := ToHTML(in)
		if strings.Contains(got, `href="javascript:`) || strings.Contains(got, `src="javascript:`) ||
			strings.Contains(got, `href="data:`) || strings.Contains(got, `src="data:`) ||
			strings.Contains(got, `href="vbscript:`) {
			t.Errorf("ToHTML(%q) produced a live non-http URL: %q", in, got)
		}
	}
}

// A digest is written by a model reading an untrusted web page. Lot titles routinely
// contain angle brackets for sizes, and a title crafted to close a tag must not be
// able to inject markup into the dashboard.
func TestHTMLIsEscaped(t *testing.T) {
	got := ToHTML("Screen <30cm and a <script>alert(1)</script> title")
	if strings.Contains(got, "<script>") {
		t.Fatalf("script tag survived escaping: %q", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("angle brackets were not escaped: %q", got)
	}
	if !strings.Contains(got, "&lt;30cm") {
		t.Errorf("a size like <30cm should render as text: %q", got)
	}
}

func TestListsCloseProperly(t *testing.T) {
	got := ToHTML("- a\n- b\n\nafter")
	if strings.Count(got, "<ul>") != 1 || strings.Count(got, "</ul>") != 1 {
		t.Errorf("unbalanced list markup: %q", got)
	}
	if !strings.Contains(got, "<p>after") {
		t.Errorf("paragraph after the list was lost: %q", got)
	}
}

func TestRealDigestShape(t *testing.T) {
	digest := `## Best find: ~25 projector screens

Projection screens are rare in bulk at auction.

### Halloween

- **5x Canon WUX500** -- DKK 750-800 each, closes 17 Aug.
  ` + "`tv-skaerme · lots 73-77`" + `
- **Panasonic PT-DW10000E** -- 10,000 lumen, DKK 4,300.

No 3D printing lots appeared this round.`

	got := ToHTML(digest)
	for _, want := range []string{"<h3>", "<h4>", "<ul>", "<li>", "<strong>", "<code>", "<p>"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered digest is missing %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "**") {
		t.Errorf("bold markers survived into the output:\n%s", got)
	}
}

// Memos can carry GFM tables; they must render as tables, not pipes as text.
func TestTables(t *testing.T) {
	got := ToHTML("| Issue | Rule |\n| :-- | :-- |\n| ENG-1 | R2 |\n")
	for _, want := range []string{"<table>", "<th", ">ENG-1</td>", ">R2</td>"} {
		if !strings.Contains(got, want) {
			t.Errorf("table missing %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "| :--") {
		t.Errorf("table delimiter row leaked as text:\n%s", got)
	}
}

func TestLinkIssues(t *testing.T) {
	links := map[string]string{"ENG": "https://linear.app/acme/issue/"}
	cases := []struct{ in, want, absent string }{
		{"see ENG-12 now", `<a class="issue" data-issue="ENG-12" href="https://linear.app/acme/issue/ENG-12" rel="noreferrer">ENG-12</a>`, ""},
		{"<code>ENG-12</code>", "<code>ENG-12</code>", `class="issue"`},
		{`<a href="https://x/">ENG-12</a>`, `<a href="https://x/">ENG-12</a>`, `class="issue"`},
		{"dates are ISO-8601 and MAR-4 is another team", "", `class="issue"`},
		{"ENG-12/ENG-13", `data-issue="ENG-13"`, ""},
	}
	for _, c := range cases {
		got := LinkIssues(c.in, links)
		if c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("LinkIssues(%q) = %q, want it to contain %q", c.in, got, c.want)
		}
		if c.absent != "" && strings.Contains(got, c.absent) {
			t.Errorf("LinkIssues(%q) = %q, must not contain %q", c.in, got, c.absent)
		}
	}
	if got := LinkIssues("ENG-1", nil); got != "ENG-1" {
		t.Errorf("no links configured must be a no-op, got %q", got)
	}
	// End to end: a table cell in a rendered memo becomes a link.
	got := LinkIssues(ToHTML("| Issue |\n| :-- |\n| ENG-7 |\n"), links)
	if !strings.Contains(got, `"><a class="issue" data-issue="ENG-7"`) {
		t.Errorf("table cell not linked:\n%s", got)
	}
}
