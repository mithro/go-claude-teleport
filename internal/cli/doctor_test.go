package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/test/fakeclaude/harness"
)

func TestDoctorPassesWithFakeClaude(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, ".claude")
	os.MkdirAll(filepath.Join(cfg, "projects"), 0o700)
	env := harness.Env(t, root, cfg, "XDG_DATA_HOME="+filepath.Join(root, "data"))
	code, out, stderr := run(t, env, "doctor")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s%s", code, out, stderr)
	}
	for _, w := range []string{"ok    claude on PATH", "2.1.247", "ok    config dir", "ok    data dir writable"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q:\n%s", w, out)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "data", "claude-teleport")); err != nil {
		t.Fatal("doctor must create the data dir")
	}
}

func TestDoctorFailsWithoutClaude(t *testing.T) {
	root := t.TempDir()
	code, out, _ := run(t, []string{"HOME=" + root, "PATH=" + t.TempDir()}, "doctor")
	if code != ExitFailed || !strings.Contains(out, "FAIL  claude on PATH") || !strings.Contains(out, "FAIL  config dir") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if code, _, stderr := run(t, []string{"HOME=" + root}, "doctor", "big-storage.example"); code != ExitUsage || !strings.Contains(stderr, "not implemented yet") {
		t.Fatalf("remote: %d %q", code, stderr)
	}
}
