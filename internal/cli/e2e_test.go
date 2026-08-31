package cli

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/test/fakeclaude/harness"
)

const e2eSID = "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"

func TestListInspectAgainstFakeClaude(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	cfg := filepath.Join(home, ".claude")
	cwd := filepath.Join(home, "github", "example", "widget")
	os.MkdirAll(cwd, 0o700)
	env := harness.Env(t, home, cfg, "FAKECLAUDE_BRANCH=feature/teleport")
	claude := filepath.Join(harness.Build(t), "claude")

	// 1. a finished -p session is idle
	c := exec.Command(claude, "-p", "make it verbose", "--session-id", e2eSID)
	c.Dir, c.Env = cwd, env
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	code, out, stderr := run(t, env, "list", "--json")
	if code != ExitOK {
		t.Fatalf("list: %d %s", code, stderr)
	}
	var rows []struct{ ID, State, Cwd, Branch string }
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 || rows[0].ID != e2eSID || rows[0].State != "idle" ||
		rows[0].Cwd != cwd || rows[0].Branch != "feature/teleport" {
		t.Fatalf("rows %+v %v\n%s", rows, err, out)
	}
	code, out, stderr = run(t, env, "inspect", e2eSID)
	if code != ExitOK || !strings.Contains(out, "(idle)") {
		t.Fatalf("inspect: %d %s %s", code, out, stderr)
	}
	if !strings.Contains(out, e2eSID+".jsonl") {
		t.Fatalf("inspect must list the transcript:\n%s", out)
	}

	// 2. the same session resumed interactively is running, with its registry name
	ic := exec.Command(claude, "--resume", e2eSID)
	ic.Dir, ic.Env = cwd, env
	stdin, _ := ic.StdinPipe()
	if err := ic.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { ic.Process.Kill(); ic.Wait() }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		code, out, _ = run(t, env, "list")
		if strings.Contains(out, "running") || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code != ExitOK || !strings.Contains(out, "a1b2c3d4  running  widget") {
		t.Fatalf("running session not listed:\n%s", out)
	}
	// inside-the-session selection: CLAUDE_CODE_SESSION_ID picks it with no args
	code, out, stderr = run(t, append(env, "CLAUDE_CODE_SESSION_ID="+e2eSID), "inspect")
	if code != ExitOK || !strings.Contains(out, "(running)") || !strings.Contains(out, "process    pid") {
		t.Fatalf("inspect current: %d %s %s", code, out, stderr)
	}
	// compare-config against a copy of the same config dir: nothing blocks
	stubClaudeVersion(t, "2.1.247")
	other := filepath.Join(root, "other", ".claude")
	os.MkdirAll(other, 0o700)
	code, out, _ = run(t, env, "compare-config", "--session", e2eSID, other)
	if code != ExitOK || !strings.Contains(out, "no configuration differences") {
		t.Fatalf("compare-config: %d\n%s", code, out)
	}
	io.WriteString(stdin, "/exit\n")
	if err := ic.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, out, _ = run(t, env, "list"); strings.Contains(out, "running") {
		t.Fatalf("session must be idle after /exit:\n%s", out)
	}
}
