package ui

import (
	"encoding/xml"
	"fmt"
	"os"
	"strings"
	"testing"
)

// The favicon exists in two places: this package's canonical SVG, and a data: URI
// inlined into the research memo template, which cannot reference a daemon route. Nothing but these tests keeps the copies honest.
var mirrors = []string{
	"../../templates/nightly-research/template.html",
}

type svgRect struct {
	X      string `xml:"x,attr"`
	Y      string `xml:"y,attr"`
	Width  string `xml:"width,attr"`
	Height string `xml:"height,attr"`
	RX     string `xml:"rx,attr"`
	Fill   string `xml:"fill,attr"`
}

type svgDoc struct {
	Tile []svgRect `xml:"rect"`
	Bars struct {
		Fill  string    `xml:"fill,attr"`
		Rects []svgRect `xml:"rect"`
	} `xml:"g"`
}

func parseFavicon(t *testing.T) svgDoc {
	t.Helper()
	var doc svgDoc
	if err := xml.Unmarshal(Favicon(), &doc); err != nil {
		t.Fatalf("favicon is not parseable XML: %v", err)
	}
	return doc
}

func TestFaviconIsWellFormed(t *testing.T) {
	doc := parseFavicon(t)
	if len(doc.Tile) != 1 {
		t.Fatalf("got %d tile rects, want exactly 1", len(doc.Tile))
	}
	if got, want := doc.Tile[0].Fill, "#8a4b2a"; got != want {
		t.Errorf("tile fill = %q, want %q (the --accent token)", got, want)
	}
	if got, want := doc.Bars.Fill, "#fbf9f5"; got != want {
		t.Errorf("bar fill = %q, want %q (the --paper token)", got, want)
	}
	if len(doc.Bars.Rects) != 4 {
		t.Fatalf("got %d bars, want 4", len(doc.Bars.Rects))
	}
}

// Every coordinate must be even. The icon is drawn on a 32-unit grid but Chrome
// renders it into 16 CSS pixels, so an odd coordinate lands on a half-pixel and the
// bar smears. Verified by rendering; this test keeps a later edit from undoing it.
func TestFaviconGridIsEven(t *testing.T) {
	doc := parseFavicon(t)
	for i, r := range doc.Bars.Rects {
		for name, v := range map[string]string{"x": r.X, "y": r.Y, "width": r.Width, "height": r.Height} {
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
				t.Errorf("bar %d: %s = %q, not an integer", i, name, v)
				continue
			}
			if n%2 != 0 {
				t.Errorf("bar %d: %s = %d is odd; odd coordinates smear at 16px", i, name, n)
			}
		}
	}
}

func TestFaviconMirrorsAreInSync(t *testing.T) {
	doc := parseFavicon(t)
	for _, path := range mirrors {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		html := string(b)

		// The data: URI must carry the same fills, with # percent-encoded or the
		// browser reads everything after it as a fragment and the icon vanishes.
		for _, fill := range []string{doc.Tile[0].Fill, doc.Bars.Fill} {
			want := "%23" + strings.TrimPrefix(fill, "#")
			if !strings.Contains(html, want) {
				t.Errorf("%s: missing encoded fill %q", path, want)
			}
		}
		if strings.Contains(html, "svg+xml,<svg") && strings.Contains(html, "fill='#") {
			t.Errorf("%s: a fill still has a raw '#'; it must be percent-encoded", path)
		}

		for i, r := range doc.Bars.Rects {
			want := fmt.Sprintf("x='%s' y='%s' width='%s' height='%s' rx='%s'", r.X, r.Y, r.Width, r.Height, r.RX)
			if !strings.Contains(html, want) {
				t.Errorf("%s: bar %d out of sync with %s\n  want inlined: %s",
					path, i, "internal/ui/assets/favicon.svg", want)
			}
		}
	}
}
