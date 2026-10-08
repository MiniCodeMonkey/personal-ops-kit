package entries

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Prepare stamps e's ts, validates it, and returns the exact JSON line
// (without trailing newline) that Append would write.
func Prepare(e Entry, now time.Time) (Entry, []byte, error) {
	e.TS = now
	if err := e.Validate(); err != nil {
		return e, nil, err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return e, nil, fmt.Errorf("marshal entry: %w", err)
	}
	return e, line, nil
}

// Append validates e, stamps its ts, and appends one JSON line to path under
// an exclusive flock. The caller never sets ts; the writer is the clock.
func Append(path string, e Entry) error {
	return appendAt(path, e, time.Now())
}

func appendAt(path string, e Entry, now time.Time) error {
	_, line, err := Prepare(e, now)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock %s: %w", path, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_, err = f.Write(line)
	return err
}
