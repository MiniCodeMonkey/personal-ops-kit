// Command lanepick chooses tonight's research lane.
//
// Lanes live one per file in lanes/<slug>.md with optional frontmatter:
//
//	---
//	pool: any name, to group lanes (optional)
//	weight: 3
//	months: all | 7-11 | 3,4,5
//	---
//	The brief, as prose.
//
// Selection is scored least-recently-used: days since the lane last appeared in the
// ledger, times its weight. A heavier lane resurfaces sooner without ever starving a
// lighter one, because an unvisited lane's score climbs every day until it wins.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// neverRunDays stands in for a lane with no ledger history. Far larger than any real
// gap, so an unvisited lane outranks every visited one whatever the weights are.
const neverRunDays = 3650

type lane struct {
	Slug   string
	Pool   string
	Weight float64
	Months map[int]bool
	Brief  string

	Days  int
	Score float64
}

var frontmatter = regexp.MustCompile(`(?s)^---\s*\n(.*?)\n---\s*\n?(.*)$`)

// parseMonths accepts "all", "7-11", "11-2" (wrapping the year end), or "3,4,5".
// Out-of-range values are dropped after expansion, so "0-13" means the whole year.
//
// A value it cannot read is an error rather than an empty set. The original raised
// ValueError here and crashed the night, which is loud and obvious; silently treating
// a typo as "no months" would be worse than either, because the lane would simply
// never be chosen again and nothing would ever say so.
func parseMonths(spec string) (map[int]bool, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		spec = "all"
	}
	months := map[int]bool{}
	if spec == "all" {
		for m := 1; m <= 12; m++ {
			months[m] = true
		}
		return months, nil
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if start, end, found := strings.Cut(part, "-"); found {
			a, errA := strconv.Atoi(strings.TrimSpace(start))
			b, errB := strconv.Atoi(strings.TrimSpace(end))
			if errA != nil || errB != nil {
				return nil, fmt.Errorf("months %q: %q is not a range of numbers", spec, part)
			}
			if a <= b {
				for m := a; m <= b; m++ {
					months[m] = true
				}
			} else {
				// A window that wraps the year end, such as 11-2 for winter.
				for m := a; m <= 12; m++ {
					months[m] = true
				}
				for m := 1; m <= b; m++ {
					months[m] = true
				}
			}
			continue
		}
		m, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("months %q: %q is not a number", spec, part)
		}
		months[m] = true
	}
	for m := range months {
		if m < 1 || m > 12 {
			delete(months, m)
		}
	}
	return months, nil
}

func loadLanes(dir string) ([]*lane, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("no lanes directory at %s", dir)
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".md") || strings.HasPrefix(name, ".") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	var lanes []*lane
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		text := string(raw)
		meta := map[string]string{}
		body := text
		if m := frontmatter.FindStringSubmatch(text); m != nil {
			for _, line := range strings.Split(m[1], "\n") {
				if key, value, found := strings.Cut(line, ":"); found {
					meta[strings.TrimSpace(key)] = strings.TrimSpace(value)
				}
			}
			body = m[2]
		}

		weight := 1.0
		if raw, ok := meta["weight"]; ok {
			if parsed, err := strconv.ParseFloat(raw, 64); err == nil {
				weight = parsed
			}
		}
		if weight < 0 {
			weight = 0
		}
		pool := meta["pool"]

		months, err := parseMonths(meta["months"])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}

		lanes = append(lanes, &lane{
			Slug:   strings.TrimSuffix(name, ".md"),
			Pool:   pool,
			Weight: weight,
			Months: months,
			Brief:  strings.TrimSpace(body),
		})
	}
	if len(lanes) == 0 {
		return nil, fmt.Errorf("no lane files found in %s", dir)
	}
	return lanes, nil
}

// lastSeen returns the most recent ledger date per lane slug.
//
// Dates are compared as strings, which is correct for zero-padded ISO dates and is
// what the original did.
func lastSeen(path string) map[string]string {
	seen := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return seen
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec struct {
			Lane string `json:"lane"`
			Date string `json:"date"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.Lane == "" || rec.Date == "" {
			continue
		}
		if current, ok := seen[rec.Lane]; !ok || rec.Date > current {
			seen[rec.Lane] = rec.Date
		}
	}
	return seen
}

func daysSince(date string, today time.Time) int {
	if date == "" {
		return neverRunDays
	}
	then, err := time.ParseInLocation("2006-01-02", date, time.Local)
	if err != nil {
		return neverRunDays
	}
	days := int(today.Sub(then).Hours() / 24)
	if days < 0 {
		return 0
	}
	return days
}

func main() {
	var (
		lanesDir   = flag.String("lanes", defaultLanesDir(), "directory of lane files")
		ledgerPath = flag.String("ledger", defaultLedger(), "ledger to score against")
		slug       = flag.String("slug", "", "force a specific lane")
		list       = flag.Bool("list", false, "show every lane with its score")
	)
	flag.Parse()

	lanes, err := loadLanes(*lanesDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	today := time.Now().Truncate(24 * time.Hour)
	todayDate := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.Local)
	seen := lastSeen(*ledgerPath)
	for _, l := range lanes {
		l.Days = daysSince(seen[l.Slug], todayDate)
		l.Score = float64(l.Days) * l.Weight
	}

	if *slug != "" {
		for _, l := range lanes {
			if l.Slug == *slug {
				fmt.Println(l.Slug)
				fmt.Println(l.Brief)
				return
			}
		}
		fmt.Fprintf(os.Stderr, "no lane with slug '%s'\n", *slug)
		os.Exit(1)
	}

	if *list {
		// Descending by score, stable so equal scores keep filename order.
		ordered := append([]*lane(nil), lanes...)
		sort.SliceStable(ordered, func(a, b int) bool { return ordered[a].Score > ordered[b].Score })
		for _, l := range ordered {
			age := fmt.Sprintf("%dd", l.Days)
			if l.Days >= neverRunDays {
				age = "never"
			}
			fmt.Printf("%-24s %-9s w=%-4s last=%-6s score=%.1f\n",
				l.Slug, l.Pool, formatWeight(l.Weight), age, l.Score)
		}
		return
	}

	// No pool argument means every lane is a candidate.
	pool := ""
	if args := flag.Args(); len(args) > 0 {
		pool = args[0]
	}

	var candidates []*lane
	for _, l := range lanes {
		if pool == "" || l.Pool == pool {
			candidates = append(candidates, l)
		}
	}
	if len(candidates) == 0 {
		fmt.Fprintf(os.Stderr, "no lanes in pool '%s'\n", pool)
		os.Exit(1)
	}

	var inSeason []*lane
	for _, l := range candidates {
		if l.Months[int(todayDate.Month())] {
			inSeason = append(inSeason, l)
		}
	}
	// Falling back to the full pool beats skipping the night entirely.
	if len(inSeason) > 0 {
		candidates = inSeason
	}

	// Highest score wins; ties break on the largest slug, matching the original's
	// max() over a (score, slug) tuple. Deterministic, so a dry run predicts the
	// real run exactly.
	winner := candidates[0]
	for _, l := range candidates[1:] {
		if l.Score > winner.Score || (l.Score == winner.Score && l.Slug > winner.Slug) {
			winner = l
		}
	}
	fmt.Println(winner.Slug)
	fmt.Println(winner.Brief)
}

// formatWeight matches Python's %g: 1 rather than 1.0, 2.5 stays 2.5.
func formatWeight(w float64) string {
	return strconv.FormatFloat(w, 'g', -1, 64)
}

func defaultLanesDir() string {
	if dir := os.Getenv("OPS_JOB_DIR"); dir != "" {
		return filepath.Join(dir, "lanes")
	}
	return "lanes"
}

func defaultLedger() string {
	for _, key := range []string{"OPS_LEDGER", "RESEARCH_LEDGER"} {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return "ledger.jsonl"
}
