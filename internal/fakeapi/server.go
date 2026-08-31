// Package fakeapi is a canned Anthropic Messages API server so Claude Code
// can run without credentials in tests (spec §12; endpoints per ENDPOINTS.md).
package fakeapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Options struct {
	Reply  string // canned assistant text
	Model  string // reported model id
	LogDir string // one file per request body, "" = memory only
}

type Request struct {
	Path string
	Body []byte
	At   time.Time
}

type Server struct {
	opts Options
	mu   sync.Mutex
	reqs []Request
	seq  int
}

func New(o Options) *Server {
	if o.Reply == "" {
		o.Reply = "Hello from the canned server."
	}
	if o.Model == "" {
		o.Model = "claude-opus-5"
	}
	return &Server{opts: o}
}

// Requests returns a copy of every recorded request.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.reqs...)
}

func (s *Server) record(r *http.Request) []byte {
	body, _ := io.ReadAll(r.Body)
	rec := Request{Path: r.URL.Path, Body: body, At: time.Now()}
	s.mu.Lock()
	s.reqs = append(s.reqs, rec)
	s.seq++
	n := s.seq
	s.mu.Unlock()
	if s.opts.LogDir != "" {
		entry, _ := json.Marshal(map[string]any{"path": rec.Path, "method": r.Method, "query": r.URL.RawQuery, "at": rec.At, "body": json.RawMessage(bodyOrNull(body))})
		_ = os.MkdirAll(s.opts.LogDir, 0o755)
		_ = os.WriteFile(filepath.Join(s.opts.LogDir, fmt.Sprintf("%04d.json", n)), entry, 0o644)
	}
	return body
}

func bodyOrNull(b []byte) []byte {
	if !json.Valid(b) {
		q, _ := json.Marshal(string(b))
		return q
	}
	return b
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body := s.record(r)
		p := r.URL.Path
		switch {
		case r.Method == http.MethodPost && strings.HasPrefix(p, "/v1/messages/count_tokens"):
			writeJSON(w, 200, map[string]int{"input_tokens": estimateTokens(body)})
		case r.Method == http.MethodPost && strings.HasPrefix(p, "/v1/messages"):
			s.handleMessages(w, body)
		case r.Method == http.MethodGet && strings.HasPrefix(p, "/v1/models"):
			writeJSON(w, 200, map[string]any{
				"data":     []map[string]any{{"id": s.opts.Model, "type": "model", "display_name": s.opts.Model, "created_at": "2026-01-01T00:00:00Z"}},
				"has_more": false, "first_id": s.opts.Model, "last_id": s.opts.Model,
			})
		case strings.HasPrefix(p, "/api/hello"):
			writeJSON(w, 200, map[string]any{"ok": true, "server": "claude-teleport fakeapi"})
		default:
			writeJSON(w, 404, map[string]any{"type": "error", "error": map[string]string{"type": "not_found_error", "message": "no such endpoint: " + r.Method + " " + p}})
		}
	})
	return mux
}

func estimateTokens(body []byte) int {
	n := len(body) / 4
	if n < 1 {
		n = 1
	}
	return n
}
