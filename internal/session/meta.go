package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Meta is the human context pulled from the transcript.
type Meta struct {
	Summary, Title, FirstUser  string
	LaunchCwd, WorkCwd, Branch string
	Version                    string
	LastTS                     string
}

// scannerBuf is the line buffer for transcripts (single records can be MBs).
const scannerBuf = 64 * 1024 * 1024

// ReadMeta scans a transcript's JSONL for a label, cwd and branch. Launch
// cwd = first cwd seen (its munge names the project folder — the directory
// to resume from); work cwd = last cwd seen. Unparseable lines are skipped.
func ReadMeta(transcript string) (Meta, error) {
	var m Meta
	f, err := os.Open(transcript)
	if err != nil {
		return m, fmt.Errorf("read transcript: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), scannerBuf)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var o struct {
			Type        string `json:"type"`
			Cwd         string `json:"cwd"`
			GitBranch   string `json:"gitBranch"`
			Version     string `json:"version"`
			Timestamp   string `json:"timestamp"`
			Summary     string `json:"summary"`
			AiTitle     string `json:"aiTitle"`
			CustomTitle string `json:"customTitle"`
			Message     struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &o) != nil {
			continue
		}
		if o.Cwd != "" {
			if m.LaunchCwd == "" {
				m.LaunchCwd = o.Cwd
			}
			m.WorkCwd = o.Cwd
		}
		if o.GitBranch != "" {
			m.Branch = o.GitBranch
		}
		if o.Version != "" {
			m.Version = o.Version
		}
		if o.Timestamp != "" && (o.Type == "user" || o.Type == "assistant") {
			m.LastTS = o.Timestamp
		}
		switch {
		case o.Type == "summary" && o.Summary != "":
			m.Summary = o.Summary
		case o.Type == "ai-title" && o.AiTitle != "":
			m.Title = o.AiTitle
		case o.Type == "custom-title" && o.CustomTitle != "":
			m.Title = o.CustomTitle
		case o.Type == "user" && m.FirstUser == "":
			m.FirstUser = firstUserText(o.Message.Content)
		}
	}
	if err := sc.Err(); err != nil {
		return m, fmt.Errorf("read transcript %s: %w", transcript, err)
	}
	return m, nil
}

// firstUserText extracts the first plain-text chunk of a user message:
// either a bare string (ignored when it looks like markup) or the first
// {"type":"text"} part of a content list.
func firstUserText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if strings.HasPrefix(strings.TrimSpace(s), "<") {
			return ""
		}
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		for _, p := range parts {
			if p.Type == "text" {
				return p.Text
			}
		}
	}
	return ""
}

// Label is the best one-line description: title > rolling summary > first
// user prompt, whitespace-collapsed and clipped to 200 runes.
func (m Meta) Label() string {
	text := m.Title
	if text == "" {
		text = m.Summary
	}
	if text == "" {
		text = m.FirstUser
	}
	if text == "" {
		text = "(no summary found)"
	}
	text = strings.Join(strings.Fields(text), " ")
	r := []rune(text)
	if len(r) > 200 {
		return string(r[:200]) + "…"
	}
	return text
}
