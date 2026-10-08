package entries

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testTime() time.Time {
	return time.Date(2026, 8, 23, 8, 0, 0, 0, time.FixedZone("CEST", 2*3600))
}

func TestAppendWritesOneValidLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "entries.jsonl")
	e := validEntry()
	e.TS = time.Time{} // append stamps it
	if err := appendAt(path, e, testTime()); err != nil {
		t.Fatalf("append: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var got Entry
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("line is not one JSON object: %v", err)
	}
	if !got.TS.Equal(testTime()) {
		t.Fatalf("ts not stamped: got %v", got.TS)
	}
	if got.ID != e.ID || got.Title != e.Title {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestAppendRejectsInvalidEntryWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entries.jsonl")
	e := validEntry()
	e.Status = "banana"
	if err := appendAt(path, e, testTime()); err == nil {
		t.Fatal("invalid entry accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("file was created for a rejected entry")
	}
}

func TestPrepareMatchesWhatAppendWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entries.jsonl")
	e := validEntry()
	e.TS = time.Time{} // append/Prepare both stamp it

	_, prepared, err := Prepare(e, testTime())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := appendAt(path, e, testTime()); err != nil {
		t.Fatalf("append: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	written := raw[:len(raw)-1] // trim the trailing newline Append adds
	if string(written) != string(prepared) {
		t.Fatalf("Prepare's line differs from what Append wrote:\n prepared: %s\n written:  %s", prepared, written)
	}
}

func TestConcurrentAppendsProduceWholeLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entries.jsonl")
	const writers = 20
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e := validEntry()
			e.ID = e.ID + string(rune('a'+i))
			if err := appendAt(path, e, testTime()); err != nil {
				t.Errorf("writer %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	lines := 0
	for sc.Scan() {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("torn or invalid line %d: %v", lines, err)
		}
		lines++
	}
	if lines != writers {
		t.Fatalf("got %d lines, want %d", lines, writers)
	}
}
