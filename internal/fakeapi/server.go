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
	errs []string
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

// Errs returns a copy of every recorded error message.
func (s *Server) Errs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.errs...)
}

// errf records an error to stderr and the error log.
func (s *Server) errf(format string, args ...any) {
	msg := fmt.Sprintf("fakeapi: "+format, args...)
	fmt.Fprintln(os.Stderr, msg)
	s.mu.Lock()
	s.errs = append(s.errs, msg)
	s.mu.Unlock()
}

func (s *Server) record(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.errf("ReadAll %s: %v", r.URL.Path, err)
		return nil, err
	}
	rec := Request{Path: r.URL.Path, Body: body, At: time.Now()}
	s.mu.Lock()
	s.reqs = append(s.reqs, rec)
	s.seq++
	n := s.seq
	s.mu.Unlock()
	if s.opts.LogDir != "" {
		entry, err := json.Marshal(map[string]any{"path": rec.Path, "method": r.Method, "query": r.URL.RawQuery, "at": rec.At, "body": json.RawMessage(bodyOrNull(body))})
		if err != nil {
			s.errf("Marshal %s: %v", rec.Path, err)
		} else {
			if err := os.MkdirAll(s.opts.LogDir, 0o755); err != nil {
				s.errf("MkdirAll %s: %v", s.opts.LogDir, err)
			} else if err := os.WriteFile(filepath.Join(s.opts.LogDir, fmt.Sprintf("%04d.json", n)), entry, 0o644); err != nil {
				s.errf("WriteFile %s: %v", s.opts.LogDir, err)
			}
		}
	}
	return body, nil
}

func bodyOrNull(b []byte) []byte {
	if !json.Valid(b) {
		q, _ := json.Marshal(string(b))
		return q
	}
	return b
}

func (s *Server) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.errf("Encode: %v", err)
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, err := s.record(r)
		if err != nil {
			s.writeJSON(w, 400, map[string]any{"type": "error", "error": map[string]string{"type": "invalid_request_error", "message": "failed to read request body"}})
			return
		}
		p := r.URL.Path
		switch {
		case r.Method == http.MethodPost && strings.HasPrefix(p, "/v1/messages/count_tokens"):
			s.writeJSON(w, 200, map[string]int{"input_tokens": estimateTokens(body)})
		case r.Method == http.MethodPost && strings.HasPrefix(p, "/v1/messages"):
			s.handleMessages(w, body)
		case r.Method == http.MethodGet && strings.HasPrefix(p, "/v1/models"):
			s.writeJSON(w, 200, map[string]any{
				"data":     []map[string]any{{"id": s.opts.Model, "type": "model", "display_name": s.opts.Model, "created_at": "2026-01-01T00:00:00Z"}},
				"has_more": false, "first_id": s.opts.Model, "last_id": s.opts.Model,
			})
		case strings.HasPrefix(p, "/api/hello"):
			s.writeJSON(w, 200, map[string]any{"ok": true, "server": "claude-teleport fakeapi"})
		default:
			s.writeJSON(w, 404, map[string]any{"type": "error", "error": map[string]string{"type": "not_found_error", "message": "no such endpoint: " + r.Method + " " + p}})
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
