package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectLocalIdleSession(t *testing.T) {
	home := t.TempDir()
	const sid = "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c"
	cwd := filepath.Join(home, "proj")
	os.MkdirAll(cwd, 0o755)
	cfg := filepath.Join(home, ".claude")
	proj := filepath.Join(cfg, "projects", "-"+strings.ReplaceAll(strings.ReplaceAll(strings.TrimPrefix(cwd, "/"), "/", "-"), ".", "-"))
	os.MkdirAll(proj, 0o700)
	os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(`{"type":"user","sessionId":"`+sid+`","cwd":"`+cwd+`","gitBranch":"","version":"2.1.247","timestamp":"2026-08-27T10:00:00Z","message":{"role":"user","content":"hi"}}`+"\n"), 0o600)
	var out, errb bytes.Buffer
	code := Main([]string{"inspect", sid}, strings.NewReader(""), &out, &errb, []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")})
	if code != ExitOK {
		t.Fatalf("inspect = %d: %s", code, errb.String())
	}
	for _, want := range []string{"3f2a9c1e", cwd, "idle", "not a git repository", "session files"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("inspect output lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if code := Main([]string{"inspect", sid, "--json"}, strings.NewReader(""), &out, &errb, []string{"HOME=" + home}); code != ExitOK || !strings.HasPrefix(strings.TrimSpace(out.String()), "{") {
		t.Errorf("--json = %d %q", code, out.String())
	}
}

// TestInspectUnknownSessionRefused covers the plain (no --host) case for a
// session that does not exist locally: it must fail before anything else
// (exit 3, matching a teleport's own session-resolution refusal).
func TestInspectUnknownSessionRefused(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".claude", "projects"), 0o700)
	code, _, stderr := run(t, []string{"HOME=" + home}, "inspect", "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c")
	if code != ExitRefused || !strings.Contains(stderr, "not") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}
