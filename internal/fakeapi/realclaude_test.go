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

// messagesRequests filters reqs down to the recorded POST /v1/messages
// bodies (path prefix, per ENDPOINTS.md: query is ignored). Auxiliary
// requests (e.g. GET /api/hello) vary with ambient environment and Claude
// Code version — see ENDPOINTS.md — so tests must not assert on them.
func messagesRequests(reqs []Request) []Request {
	var out []Request
	for _, r := range reqs {
		if strings.HasPrefix(r.Path, "/v1/messages") {
			out = append(out, r)
		}
	}
	return out
}

// TestRealClaudeAgainstFakeAPI pins ENDPOINTS.md's stable facts: `-p` makes
// at least one POST /v1/messages carrying the prompt, a transcript lands on
// disk, and `--resume` makes a further POST /v1/messages whose body carries
// both the original prompt and the new one. The count and order of any other
// requests (e.g. GET /api/hello) is not pinned — see ENDPOINTS.md.
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
	paths := make([]string, len(reqs))
	for i, r := range reqs {
		paths[i] = r.Path
	}
	t.Logf("observed requests after -p: %v", paths)
	msgs := messagesRequests(reqs)
	if len(msgs) == 0 {
		t.Fatalf("expected at least one POST /v1/messages, got none among: %v", paths)
	}
	last := msgs[len(msgs)-1]
	if !strings.Contains(string(last.Body), `"stream":true`) || !strings.Contains(string(last.Body), "say hello") {
		t.Errorf("last /v1/messages request = %.200s", last.Body)
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
	paths = make([]string, len(reqs))
	for i, r := range reqs {
		paths[i] = r.Path
	}
	t.Logf("observed requests after --resume: %v", paths)
	msgsAfterResume := messagesRequests(reqs)
	if len(msgsAfterResume) <= len(msgs) {
		t.Fatalf("expected at least one additional POST /v1/messages after --resume, had %d before and %d after: %v", len(msgs), len(msgsAfterResume), paths)
	}
	resumed := msgsAfterResume[len(msgsAfterResume)-1]
	if !strings.Contains(string(resumed.Body), "say hello") || !strings.Contains(string(resumed.Body), "what did I say?") {
		t.Errorf("resume request must carry the prior conversation: %.400s", resumed.Body)
	}
	if _, err := os.Stat(filepath.Join(configDir, ".credentials.json")); err == nil {
		t.Errorf("a credentials file appeared in the throw-away config dir — the test must never touch real credentials")
	}
}
