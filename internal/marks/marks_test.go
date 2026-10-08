package marks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestToggleRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "bookmarks.json")
	s := New(path)

	if s.Has("research", "memo.html") {
		t.Fatal("fresh store has a mark")
	}
	on, err := s.Toggle("research", "memo.html")
	if err != nil || !on {
		t.Fatalf("toggle on = %v, %v", on, err)
	}
	if !s.Has("research", "memo.html") {
		t.Error("mark not set after toggle")
	}

	// Persisted: a new store over the same file sees it.
	if !New(path).Has("research", "memo.html") {
		t.Error("mark did not survive reload")
	}

	on, err = s.Toggle("research", "memo.html")
	if err != nil || on {
		t.Fatalf("toggle off = %v, %v", on, err)
	}
	if New(path).Has("research", "memo.html") {
		t.Error("mark survived removal")
	}
}

func TestMissingOrCorruptFileStartsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bookmarks.json")
	if err := os.WriteFile(path, []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(path)
	if s.Has("a", "b") {
		t.Error("corrupt file yielded a mark")
	}
	if _, err := s.Toggle("a", "b"); err != nil {
		t.Fatalf("toggle after corrupt file: %v", err)
	}
}

func TestNilStoreIsSafe(t *testing.T) {
	var s *Store
	if s.Has("a", "b") {
		t.Error("nil store has a mark")
	}
	if _, err := s.Toggle("a", "b"); err == nil {
		t.Error("nil store toggle should error")
	}
}
