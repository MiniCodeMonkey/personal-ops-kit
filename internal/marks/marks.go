// Package marks keeps the artifacts the reader has bookmarked.
//
// A bookmark is a reader-side fact, not a job-side one, so it does not belong in the
// job's ledger: the ledger records what a run did, and starring a memo a week later
// is not something the run did. One small JSON file under the artifacts root holds
// every mark, keyed by job and file, and is rewritten whole on every change -- it
// will never hold more than a few dozen entries.
package marks

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Store is the set of bookmarked artifacts, backed by one file.
type Store struct {
	path string
	mu   sync.Mutex
}

// New returns a store over path. The file need not exist yet.
func New(path string) *Store { return &Store{path: path} }

// Key names an artifact the way the store does.
func Key(job, file string) string { return job + "/" + file }

// Has reports whether the artifact is bookmarked. A nil store has no marks.
func (s *Store) Has(job, file string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()[Key(job, file)]
}

// Toggle flips the artifact's bookmark and reports its new state.
func (s *Store) Toggle(job, file string) (bool, error) {
	if s == nil {
		return false, errors.New("no bookmark store")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	set := s.load()
	key := Key(job, file)
	on := !set[key]
	if on {
		set[key] = true
	} else {
		delete(set, key)
	}
	return on, s.save(set)
}

// load reads the file each time rather than caching, so a hand edit or a second
// process is honoured. A missing or unreadable file is an empty set: losing every
// bookmark to one bad byte would be worse than starting over.
func (s *Store) load() map[string]bool {
	set := map[string]bool{}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return set
	}
	var keys []string
	if err := json.Unmarshal(b, &keys); err != nil {
		return set
	}
	for _, k := range keys {
		set[k] = true
	}
	return set
}

func (s *Store) save(set map[string]bool) error {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
