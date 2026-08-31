package job

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// HistoryRecord is one line of jobs/<id>/history.jsonl (spec §6 step 10).
type HistoryRecord struct {
	At        time.Time `json:"at"`
	SessionID string    `json:"session_id"`
	Direction string    `json:"direction"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Outcome   string    `json:"outcome"`
	Note      string    `json:"note,omitempty"`
}

// AppendHistory appends r to dir/history.jsonl, creating dir (0700) if needed.
func AppendHistory(dir string, r HistoryRecord) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("history dir %s: %w", dir, err)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encode history record: %w", err)
	}
	path := filepath.Join(dir, "history.jsonl")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("append history %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("append history %s: %w", path, err)
	}
	return nil
}
