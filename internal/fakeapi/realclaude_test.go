//go:build realclaude

package fakeapi

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/session"
)

func freshUUID(t *testing.T) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// TestRealClaudeAgainstFakeAPI pins ENDPOINTS.md: a fixed request sequence
// (/api/hello then POST /v1/messages) for `-p`, a transcript on disk, and
// prior context carried across --resume.
func TestRealClaudeAgainstFakeAPI(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not on PATH")
	}
	repoRoot, _ := filepath.Abs(filepath.Join("..", ".."))
	tmpRoot := filepath.Join(repoRoot, "tmp")
	os.MkdirAll(tmpRoot, 0o755)
	configDir, err := os.MkdirTemp(tmpRoot, "fakeapi-cfg-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(configDir)
	cwd, _ := os.MkdirTemp(tmpRoot, "fakeapi-cwd-")
	defer os.RemoveAll(cwd)

	s := New(Options{Reply: "Hello from the canned server.", Model: "claude-opus-5"})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	sid := freshUUID(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, errOut, err := RunClaude(ctx, ts.URL, configDir, cwd, "-p", "--session-id", sid, "say hello")
	if err != nil {
		t.Fatalf("claude -p: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	if !strings.Contains(string(out), "Hello from the canned server.") {
		t.Errorf("claude did not print the canned reply: %q", out)
	}
	reqs := s.Requests()
	// As of Claude Code 2.1.251, a fresh CLAUDE_CONFIG_DIR makes claude probe
	// GET /api/hello before POST /v1/messages (see ENDPOINTS.md's 2026-08-31
	// update) — update the expected paths here if that count changes again.
	if len(reqs) != 2 {
		paths := make([]string, len(reqs))
		for i, r := range reqs {
			paths[i] = r.Path
		}
		t.Fatalf("expected exactly two requests, got %d: %v — update ENDPOINTS.md if Claude Code changed", len(reqs), paths)
	}
	if reqs[0].Path != "/api/hello" {
		t.Errorf("first request = %s, want /api/hello", reqs[0].Path)
	}
	if reqs[1].Path != "/v1/messages" || !strings.Contains(string(reqs[1].Body), `"stream":true`) || !strings.Contains(string(reqs[1].Body), "say hello") {
		t.Errorf("second request = %s %.200s", reqs[1].Path, reqs[1].Body)
	}
	transcript := filepath.Join(configDir, "projects", session.Munge(cwd), sid+".jsonl")
	if fi, err := os.Stat(transcript); err != nil || fi.Size() == 0 {
		t.Fatalf("transcript %s: %v", transcript, err)
	}

	out, errOut, err = RunClaude(ctx, ts.URL, configDir, cwd, "-p", "--resume", sid, "what did I say?")
	if err != nil {
		t.Fatalf("claude --resume: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	reqs = s.Requests()
	if len(reqs) != 4 {
		paths := make([]string, len(reqs))
		for i, r := range reqs {
			paths[i] = r.Path
		}
		t.Fatalf("expected four requests after resume, got %d: %v", len(reqs), paths)
	}
	if reqs[2].Path != "/api/hello" || reqs[3].Path != "/v1/messages" {
		t.Errorf("resume requests = %s, %s, want /api/hello, /v1/messages", reqs[2].Path, reqs[3].Path)
	}
	if !strings.Contains(string(reqs[3].Body), "say hello") || !strings.Contains(string(reqs[3].Body), "what did I say?") {
		t.Errorf("resume request must carry the prior conversation: %.400s", reqs[3].Body)
	}
	if _, err := os.Stat(filepath.Join(configDir, ".credentials.json")); err == nil {
		t.Errorf("a credentials file appeared in the throw-away config dir — the test must never touch real credentials")
	}
}
