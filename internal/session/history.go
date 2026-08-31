package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

type historyKey struct {
	Timestamp json.RawMessage `json:"timestamp"`
	SessionID string          `json:"sessionId"`
}

func (k historyKey) key() string { return string(bytes.TrimSpace(k.Timestamp)) + "|" + k.SessionID }

// ExtractHistory returns the raw lines of history.jsonl whose sessionId is
// id. A missing file yields nothing.
func ExtractHistory(historyFile string, id ID) ([]json.RawMessage, error) {
	f, err := os.Open(historyFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	defer f.Close()
	var out []json.RawMessage
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), scannerBuf)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		var k historyKey
		if len(line) == 0 || json.Unmarshal(line, &k) != nil || k.SessionID != string(id) {
			continue
		}
		out = append(out, json.RawMessage(bytes.Clone(line)))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read history %s: %w", historyFile, err)
	}
	return out, nil
}

// AppendHistory appends lines not already present (matched on
// timestamp+sessionId). The file is created if absent; a missing trailing
// newline on the existing file is repaired first.
func AppendHistory(historyFile string, lines []json.RawMessage) (added int, err error) {
	existing := map[string]bool{}
	needsNewline := false
	if data, rerr := os.ReadFile(historyFile); rerr == nil {
		for _, line := range bytes.Split(data, []byte("\n")) {
			var k historyKey
			if len(bytes.TrimSpace(line)) > 0 && json.Unmarshal(line, &k) == nil {
				existing[k.key()] = true
			}
		}
		needsNewline = len(data) > 0 && data[len(data)-1] != '\n'
	} else if !errors.Is(rerr, fs.ErrNotExist) {
		return 0, fmt.Errorf("read history: %w", rerr)
	}
	f, err := os.OpenFile(historyFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return 0, fmt.Errorf("open history: %w", err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	if needsNewline {
		w.WriteString("\n")
	}
	for _, line := range lines {
		var k historyKey
		if json.Unmarshal(line, &k) != nil {
			return added, fmt.Errorf("append history: line is not JSON: %.80s", line)
		}
		if existing[k.key()] {
			continue
		}
		existing[k.key()] = true
		w.Write(bytes.TrimSpace(line))
		w.WriteString("\n")
		added++
	}
	if err := w.Flush(); err != nil {
		return added, fmt.Errorf("write history %s: %w", historyFile, err)
	}
	return added, nil
}
