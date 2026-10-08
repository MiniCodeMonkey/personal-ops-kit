// Package ui holds the dashboard's templates and compiled stylesheet, embedded in the
// binary so the daemon has no asset paths to resolve at runtime.
package ui

import (
	"embed"
	_ "embed"
	"html/template"
	"math"
	"path"
	"strings"
)

// Job icons are Heroicons (MIT, tailwindlabs/heroicons) outline set, one file per
// name, embedded so a job.env can pick one by name without the dashboard fetching
// anything. Unknown names fall back to the cube.
//
//go:embed icons/*.svg
var iconFiles embed.FS

const defaultIcon = "cube"

// Icon returns the inline SVG for a heroicon name, sized by the caller's CSS.
func Icon(name string) template.HTML {
	if name == "" {
		name = defaultIcon
	}
	b, err := iconFiles.ReadFile(path.Join("icons", name+".svg"))
	if err != nil {
		b, _ = iconFiles.ReadFile(path.Join("icons", defaultIcon+".svg"))
	}
	svg := strings.TrimSpace(string(b))
	// Let the class hook in; the files ship without one.
	svg = strings.Replace(svg, "<svg ", `<svg class="icon" `, 1)
	return template.HTML(svg)
}

//go:embed templates/dashboard.html
var dashboardTemplate string

//go:embed assets/app.css
var css string

//go:embed assets/favicon.svg
var favicon []byte

// CSS is the compiled Tailwind stylesheet, inlined into the page.
func CSS() template.CSS { return template.CSS(css) }

// Favicon is the dashboard's site icon: the run strip reduced to a silhouette.
//
// Served from a route rather than inlined as a data: URI like the stylesheet, because
// the point of having one is the Chrome bookmarks bar and Chrome is unreliable about
// persisting data: favicons for bookmarks specifically. It is still compiled in, so
// the daemon has nothing to resolve on disk.
func Favicon() []byte { return favicon }

// Templates parses the dashboard templates. Parsed once at startup so a broken
// template fails loudly on boot rather than on the first request.
func Templates() (*template.Template, error) {
	return template.New("dashboard").Funcs(template.FuncMap{
		"seq": func(n int) []int {
			out := make([]int, n)
			for i := range out {
				out[i] = i
			}
			return out
		},
		"mul":  func(a, b int) int { return a * b },
		"icon": Icon,
		// The empty inbox's horizon: the run strip's ticks, easing to nothing at the
		// right edge. Heights follow a slow wave so it reads as settled, not random.
		"tickH": func(i int) int {
			h := 6 + 8*math.Abs(math.Sin(float64(i)*0.55))
			fade := 1 - math.Pow(float64(i)/40, 2.2)
			return int(math.Round(math.Max(2, h*fade)))
		},
		"tickY": func(i int) int {
			h := 6 + 8*math.Abs(math.Sin(float64(i)*0.55))
			fade := 1 - math.Pow(float64(i)/40, 2.2)
			return 40 - int(math.Round(math.Max(2, h*fade)))
		},
	}).Parse(dashboardTemplate)
}
