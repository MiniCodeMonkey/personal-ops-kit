package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// verify closes the loop the miner opened: a suggestion whose artifact now
// exists on disk is adopted. Positive evidence only -- absence of new
// occurrences never closes anything (that is the compiler's staleness view).

type foldInput struct {
	Items []struct {
		Kind   string `json:"kind"`
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"items"`
}

func runVerify(home string, in io.Reader, out io.Writer) error {
	var fold foldInput
	if err := json.NewDecoder(in).Decode(&fold); err != nil {
		return fmt.Errorf("verify: fold input is not JSON: %w", err)
	}
	enc := json.NewEncoder(out)
	for _, item := range fold.Items {
		if item.Status != "open" {
			continue
		}
		if adopted(home, item.Kind, item.ID) {
			out := struct {
				Kind string `json:"kind"`
				ID   string `json:"id"`
			}{Kind: item.Kind, ID: item.ID}
			if err := enc.Encode(out); err != nil {
				return err
			}
		}
	}
	return nil
}

// slugOf returns the part after the first '#': "repeated-correction#prefer-uv"
// -> "prefer-uv". An id with no '#' yields "".
func slugOf(id string) string {
	if i := strings.Index(id, "#"); i >= 0 {
		return id[i+1:]
	}
	return ""
}

func adopted(home, kind, id string) bool {
	slug := slugOf(id)
	if slug == "" {
		return false
	}
	switch kind {
	case "repeated-correction":
		var words []string
		for _, w := range strings.Split(strings.ToLower(slug), "-") {
			if w != "" {
				words = append(words, w)
			}
		}
		if len(words) == 0 {
			return false
		}
		var files []string
		// Deliberately scoped to profile-root CLAUDE.md and memory indexes only --
		// project-repo CLAUDE.md files are out of scope. A miss here is a safe
		// false negative: it just leaves the suggestion open for another look.
		for _, pattern := range []string{
			filepath.Join(home, ".claude*", "CLAUDE.md"),
			filepath.Join(home, ".claude*", "projects", "*", "memory", "MEMORY.md"),
		} {
			m, _ := filepath.Glob(pattern)
			files = append(files, m...)
		}
		for _, f := range files {
			raw, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			content := strings.ToLower(string(raw))
			all := true
			for _, w := range words {
				if !strings.Contains(content, w) {
					all = false
					break
				}
			}
			if all {
				return true
			}
		}
	case "workflow-skill-candidate":
		m, _ := filepath.Glob(filepath.Join(home, ".claude*", "skills", "*"+slug+"*"))
		for _, d := range m {
			if info, err := os.Stat(d); err == nil && info.IsDir() {
				return true
			}
		}
	}
	return false
}
