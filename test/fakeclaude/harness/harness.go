// Package harness builds test/fakeclaude and puts it on PATH as `claude`.
package harness

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

var (
	once   sync.Once
	binDir string
	bErr   error
)

// Build compiles the fake claude once per test process and returns a
// directory containing the `claude` binary.
func Build(t testing.TB) string {
	t.Helper()
	once.Do(func() {
		_, self, _, _ := runtime.Caller(0)
		pkgDir := filepath.Dir(filepath.Dir(self)) // test/fakeclaude
		dir, err := os.MkdirTemp("", "fakeclaude-")
		if err != nil {
			bErr = err
			return
		}
		cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "claude"), ".")
		cmd.Dir = pkgDir
		if out, err := cmd.CombinedOutput(); err != nil {
			bErr = err
			t.Logf("go build fakeclaude: %s", out)
			return
		}
		binDir = dir
	})
	if bErr != nil {
		t.Fatalf("build fakeclaude: %v", bErr)
	}
	return binDir
}

// Env returns a minimal environment with the fake claude first on PATH.
func Env(t testing.TB, home, configDir string, extra ...string) []string {
	t.Helper()
	env := []string{
		"PATH=" + Build(t) + ":" + os.Getenv("PATH"),
		"HOME=" + home,
		"CLAUDE_CONFIG_DIR=" + configDir,
		"TERM=dumb",
	}
	for _, e := range extra {
		if !strings.Contains(e, "=") {
			t.Fatalf("bad env entry %q", e)
		}
		env = append(env, e)
	}
	return env
}
