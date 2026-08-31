package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

const phSID = "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13"

func placeholderFixture(t *testing.T) (cfg, cwd string) {
	t.Helper()
	root := t.TempDir()
	cwd = filepath.Join(root, "work")
	os.MkdirAll(cwd, 0o700)
	cfg = filepath.Join(root, "cfg")
	proj := filepath.Join(cfg, "projects", session.Munge(cwd))
	os.MkdirAll(proj, 0o700)
	os.WriteFile(filepath.Join(proj, phSID+".jsonl"),
		[]byte(`{"type":"user","cwd":"`+cwd+`","sessionId":"`+phSID+`","message":{"content":"hi"}}`+"\n"), 0o600)
	return cfg, cwd
}

func stubExec(t *testing.T) (execs *[][]string, chdirs *[]string) {
	t.Helper()
	oldExec, oldLook, oldChdir, oldIn, oldOut := execveFn, lookPathFn, chdirFn, stdinTTYFn, stdoutTTYFn
	t.Cleanup(func() { execveFn, lookPathFn, chdirFn, stdinTTYFn, stdoutTTYFn = oldExec, oldLook, oldChdir, oldIn, oldOut })
	var e [][]string
	var c []string
	execveFn = func(path string, argv []string, env []string) error { e = append(e, append([]string{path}, argv...)); return nil }
	lookPathFn = func(file string) (string, error) { return "/usr/local/bin/" + file, nil }
	chdirFn = func(dir string) error { c = append(c, dir); return nil }
	stdinTTYFn = func() bool { return false }
	stdoutTTYFn = func() bool { return false }
	return &e, &c
}

func TestPlaceholderExecsClaude(t *testing.T) {
	cfg, cwd := placeholderFixture(t)
	execs, chdirs := stubExec(t)
	code, out, stderr := run(t, []string{"HOME=" + filepath.Dir(cwd), "CLAUDE_CONFIG_DIR=" + cfg},
		"placeholder", "--resume", phSID, "--teleported-to", "big-storage.example")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if len(*execs) != 1 || strings.Join((*execs)[0], " ") != "/usr/local/bin/claude claude --resume "+phSID {
		t.Fatalf("execs = %q", *execs)
	}
	if len(*chdirs) != 1 || (*chdirs)[0] != cwd {
		t.Fatalf("chdirs = %q", *chdirs)
	}
	if !strings.Contains(out, "teleported to big-storage.example") {
		t.Fatalf("banner:\n%s", out)
	}
}

func TestPlaceholderSavedOutputAndMissingClaude(t *testing.T) {
	cfg, cwd := placeholderFixture(t)
	execs, _ := stubExec(t)
	lookPathFn = func(string) (string, error) { return "", os.ErrNotExist }
	saved := filepath.Join(t.TempDir(), "cap.txt")
	os.WriteFile(saved, []byte("captured pane\n"), 0o600)
	code, out, stderr := run(t, []string{"HOME=" + filepath.Dir(cwd), "CLAUDE_CONFIG_DIR=" + cfg},
		"placeholder", "--resume", phSID, "--saved-output", saved, "--now")
	if code != ExitFailed || !strings.Contains(stderr, "`claude` not found") || len(*execs) != 0 {
		t.Fatalf("exit %d stderr %q execs %q", code, stderr, *execs)
	}
	if !strings.HasPrefix(out, "captured pane\n") {
		t.Fatalf("saved output first:\n%s", out)
	}
}

func TestPlaceholderRequiresResume(t *testing.T) {
	if code, _, _ := run(t, []string{"HOME=/home/alice"}, "placeholder"); code != ExitUsage {
		t.Fatalf("exit %d", code)
	}
}
