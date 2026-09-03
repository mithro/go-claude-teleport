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
//
// The build directory is a FIXED path under the repository's gitignored
// tmp/, not a fresh os.MkdirTemp: the binary is cached for the whole test
// process and shared by later tests, so no single test's t.Cleanup may
// remove it, and every package that uses this harness would otherwise
// leave one more copy behind in the system temp directory on every run
// (finding A19). Reusing one path bounds that at a single directory, and
// the build goes to a unique file that is then RENAMED into place, so two
// package test binaries building concurrently cannot see a half-written
// `claude` (rename is atomic, and it does not disturb a copy another
// process is already executing).
func Build(t testing.TB) string {
	t.Helper()
	once.Do(func() {
		_, self, _, _ := runtime.Caller(0)
		pkgDir := filepath.Dir(filepath.Dir(self))     // test/fakeclaude
		repoRoot := filepath.Dir(filepath.Dir(pkgDir)) // <repo>
		dir := filepath.Join(repoRoot, "tmp", "fakeclaude-bin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			bErr = err
			return
		}
		tmp, err := os.CreateTemp(dir, "claude-")
		if err != nil {
			bErr = err
			return
		}
		tmp.Close()
		cmd := exec.Command("go", "build", "-o", tmp.Name(), ".")
		cmd.Dir = pkgDir
		if out, err := cmd.CombinedOutput(); err != nil {
			os.Remove(tmp.Name())
			bErr = err
			t.Logf("go build fakeclaude: %s", out)
			return
		}
		if err := os.Rename(tmp.Name(), filepath.Join(dir, "claude")); err != nil {
			os.Remove(tmp.Name())
			bErr = err
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
