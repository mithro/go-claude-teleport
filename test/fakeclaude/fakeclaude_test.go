package main_test

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/test/fakeclaude/harness"
)

const sid = "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13"

type env struct{ home, cfg, cwd string }

func setup(t *testing.T) env {
	t.Helper()
	root := t.TempDir()
	e := env{home: filepath.Join(root, "home"), cfg: filepath.Join(root, "home", ".claude"), cwd: filepath.Join(root, "home", "proj")}
	os.MkdirAll(e.cwd, 0o700)
	return e
}

func (e env) cmd(t *testing.T, extra []string, args ...string) *exec.Cmd {
	t.Helper()
	c := exec.Command(filepath.Join(harness.Build(t), "claude"), args...)
	c.Dir = e.cwd
	c.Env = harness.Env(t, e.home, e.cfg, extra...)
	return c
}

func (e env) transcript(id string) string {
	return filepath.Join(e.cfg, "projects", session.Munge(e.cwd), id+".jsonl")
}

func lines(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad line %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func registry(e env, pid int) (session.Registry, bool) {
	r, err := session.ReadRegistryFile(filepath.Join(e.cfg, "sessions", strconv.Itoa(pid)+".json"))
	return r, err == nil
}

func TestVersion(t *testing.T) {
	e := setup(t)
	out, err := e.cmd(t, nil, "--version").Output()
	if err != nil || strings.TrimSpace(string(out)) != "2.1.247 (Claude Code)" {
		t.Fatalf("%q %v", out, err)
	}
	out, _ = e.cmd(t, []string{"FAKECLAUDE_VERSION=2.1.250"}, "--version").Output()
	if strings.TrimSpace(string(out)) != "2.1.250 (Claude Code)" {
		t.Fatalf("%q", out)
	}
}

func TestPrintMode(t *testing.T) {
	e := setup(t)
	out, err := e.cmd(t, []string{"FAKECLAUDE_REPLY=hello back"}, "-p", "hello", "--session-id", sid).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	recs := lines(t, e.transcript(sid))
	if len(recs) != 3 || recs[0]["type"] != "permission-mode" || recs[1]["type"] != "user" || recs[2]["type"] != "assistant" {
		t.Fatalf("%+v", recs)
	}
	if recs[0]["permissionMode"] == nil || recs[0]["sessionId"] != sid {
		t.Fatalf("permission-mode record %+v", recs[0])
	}
	u := recs[1]
	if u["cwd"] != e.cwd || u["sessionId"] != sid || u["version"] != "2.1.247" || u["gitBranch"] != "main" || u["timestamp"] == nil || u["uuid"] == nil {
		t.Fatalf("user record %+v", u)
	}
	if u["message"].(map[string]any)["content"] != "hello" {
		t.Fatalf("prompt not recorded: %+v", u)
	}
	content := recs[2]["message"].(map[string]any)["content"].([]any)[0].(map[string]any)
	if content["text"] != "hello back" || recs[2]["parentUuid"] != u["uuid"] {
		t.Fatalf("assistant record %+v", recs[2])
	}
	if entries, _ := os.ReadDir(filepath.Join(e.cfg, "sessions")); len(entries) != 0 {
		t.Fatalf("registry must be removed after -p: %v", entries)
	}
	hist := lines(t, filepath.Join(e.cfg, "history.jsonl"))
	if len(hist) != 1 || hist[0]["display"] != "hello" || hist[0]["sessionId"] != sid || hist[0]["project"] != e.cwd {
		t.Fatalf("history %+v", hist)
	}
	// resume appends to the same file (no extra permission-mode record)
	if out, err := e.cmd(t, nil, "-p", "again", "--resume", sid).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if got := lines(t, e.transcript(sid)); len(got) != 5 || got[3]["message"].(map[string]any)["content"] != "again" {
		t.Fatalf("%+v", got)
	}
	if out, err := e.cmd(t, nil, "-p", "x", "--resume", "00000000-0000-4000-8000-000000000000").CombinedOutput(); err == nil || !strings.Contains(string(out), "No conversation found") {
		t.Fatalf("unknown resume: %v %s", err, out)
	}
}

func TestNotLoggedIn(t *testing.T) {
	e := setup(t)
	out, err := e.cmd(t, []string{"FAKECLAUDE_FAIL=not-logged-in"}, "-p", "hi").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "Not logged in") {
		t.Fatalf("%v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(e.cfg, "sessions")); err == nil {
		t.Fatal("registry dir must not be created")
	}
}

func TestInteractive(t *testing.T) {
	e := setup(t)
	c := e.cmd(t, nil, "--session-id", sid)
	stdin, _ := c.StdinPipe()
	c.Stdout, c.Stderr = io.Discard, os.Stderr
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { c.Process.Kill(); c.Wait() }()
	pid := c.Process.Pid
	var r session.Registry
	waitFor(t, "registry idle", func() bool {
		var ok bool
		r, ok = registry(e, pid)
		return ok && r.Status == "idle"
	})
	if r.PID != pid || r.SessionID != sid || r.Cwd != e.cwd || r.ProcStart == "" || r.Version != "2.1.247" || r.Name != "proj" || r.Tmux != "" {
		t.Fatalf("%+v", r)
	}
	if st, _ := session.ProcStartTime("/proc", pid); st != r.ProcStart {
		t.Fatalf("procStart %s != real %s", r.ProcStart, st)
	}
	if recs := lines(t, e.transcript(sid)); len(recs) != 1 || recs[0]["type"] != "permission-mode" {
		t.Fatalf("session must open with a permission-mode record: %+v", recs)
	}
	io.WriteString(stdin, "first turn\n")
	waitFor(t, "three records", func() bool { b, _ := os.ReadFile(e.transcript(sid)); return strings.Count(string(b), "\n") == 3 })
	io.WriteString(stdin, "/exit\n")
	if err := c.Wait(); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if _, ok := registry(e, pid); ok {
		t.Fatal("registry must be removed on /exit")
	}
}

func TestSigtermCleansUp(t *testing.T) {
	e := setup(t)
	c := e.cmd(t, nil)
	stdin, _ := c.StdinPipe()
	defer stdin.Close()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	pid := c.Process.Pid
	waitFor(t, "registry", func() bool { _, ok := registry(e, pid); return ok })
	c.Process.Signal(syscall.SIGTERM)
	if err := c.Wait(); err != nil {
		t.Fatalf("SIGTERM must exit 0: %v", err)
	}
	if _, ok := registry(e, pid); ok {
		t.Fatal("registry must be removed on SIGTERM")
	}
	hits, _ := filepath.Glob(filepath.Join(e.cfg, "projects", "*", "*.jsonl"))
	if len(hits) != 1 {
		t.Fatalf("a fresh uuid session must have been created: %v", hits)
	}
	recs := lines(t, hits[0])
	if len(recs) != 1 || recs[0]["type"] != "permission-mode" || recs[0]["permissionMode"] == nil || recs[0]["sessionId"] == nil {
		t.Fatalf("session must open with a permission-mode record: %+v", recs)
	}
}

func TestRunChild(t *testing.T) {
	e := setup(t)
	out := filepath.Join(t.TempDir(), "child.txt")
	c := e.cmd(t, []string{"FAKECLAUDE_RUN_CHILD=sleep 1; echo \"$CLAUDE_PID $CLAUDE_CODE_SESSION_ID $CLAUDECODE\" > " + out}, "--session-id", sid)
	stdin, _ := c.StdinPipe()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { c.Process.Kill(); c.Wait() }()
	pid := c.Process.Pid
	waitFor(t, "busy while child runs", func() bool { r, ok := registry(e, pid); return ok && r.Status == "busy" })
	waitFor(t, "idle after child", func() bool { r, ok := registry(e, pid); return ok && r.Status == "idle" })
	got, _ := os.ReadFile(out)
	if strings.TrimSpace(string(got)) != strconv.Itoa(pid)+" "+sid+" 1" {
		t.Fatalf("child env: %q", got)
	}
	io.WriteString(stdin, "/exit\n")
	c.Wait()
}

// The tmux field is exercised only when a tmux server is reachable.
func TestTmuxField(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	e := setup(t)
	sock := filepath.Join(t.TempDir(), "sock")
	tm := func(args ...string) string {
		out, err := exec.Command("tmux", append([]string{"-S", sock}, args...)...).Output()
		if err != nil {
			t.Fatalf("tmux %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	tm("new-session", "-d", "-s", "ft", "-x", "80", "-y", "24")
	defer exec.Command("tmux", "-S", sock, "kill-server").Run()
	pane := tm("display-message", "-p", "-t", "ft:0", "#{pane_id}")
	c := e.cmd(t, []string{"TMUX=" + sock + ",1,0", "TMUX_PANE=" + pane}, "--session-id", sid)
	stdin, _ := c.StdinPipe()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { c.Process.Kill(); c.Wait() }()
	var r session.Registry
	waitFor(t, "registry", func() bool { var ok bool; r, ok = registry(e, c.Process.Pid); return ok })
	if sess, win, p, ok := r.TmuxParts(); !ok || sess != "ft" || !strings.HasPrefix(win, "@") || p != pane {
		t.Fatalf("tmux field %q", r.Tmux)
	}
	io.WriteString(stdin, "/exit\n")
	c.Wait()
}

// TestRegistryEntrypoint pins what real Claude Code 2.1.247/2.1.259 write
// (captured by the layer-2 suite, task-26-report.md): "kind" is
// "interactive" for BOTH an interactive session and a `-p` run, and only
// "entrypoint" tells them apart — "cli" vs "sdk-cli". internal/remote's
// ConfirmClaude gates spec §6.2 case 3 on exactly that field, so the fake
// has to be faithful about it. The print-mode half reads the registry from
// inside the run itself (the entry is removed the moment `-p` finishes),
// using the same FAKECLAUDE_RUN_CHILD hook TestRunChild uses.
func TestRegistryEntrypoint(t *testing.T) {
	e := setup(t)
	c := e.cmd(t, nil, "--session-id", sid)
	stdin, _ := c.StdinPipe()
	c.Stdout, c.Stderr = io.Discard, os.Stderr
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { c.Process.Kill(); c.Wait() }()
	var r session.Registry
	waitFor(t, "registry idle", func() bool {
		var ok bool
		r, ok = registry(e, c.Process.Pid)
		return ok && r.Status == "idle"
	})
	if r.Kind != "interactive" || r.Entrypoint != "cli" {
		t.Errorf("interactive registry kind=%q entrypoint=%q, want interactive/cli", r.Kind, r.Entrypoint)
	}
	io.WriteString(stdin, "/exit\n")
	c.Wait()

	snap := filepath.Join(t.TempDir(), "registry.json")
	psid := "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f14"
	out, err := e.cmd(t, []string{"FAKECLAUDE_RUN_CHILD=cp \"$CLAUDE_CONFIG_DIR/sessions/$CLAUDE_PID.json\" " + snap},
		"-p", "hello", "--session-id", psid).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	data, err := os.ReadFile(snap)
	if err != nil {
		t.Fatal(err)
	}
	var pr session.Registry
	if err := json.Unmarshal(data, &pr); err != nil {
		t.Fatal(err)
	}
	if pr.Kind != "interactive" || pr.Entrypoint != "sdk-cli" {
		t.Errorf("print-mode registry kind=%q entrypoint=%q, want interactive/sdk-cli: %s", pr.Kind, pr.Entrypoint, data)
	}
}

// TestTrustPromptModeWaitsForDownThenEnter covers FAKECLAUDE_TRUST_PROMPT
// (added for ruling R-P3-TRUST-1): the dialog is printed and NO registry
// entry exists until it is answered — the shape of a real Claude Code
// first run, and what makes a destination stuck on it invisible to the
// confirm step. Down+Enter answers "Yes, I trust this folder" and the
// session comes up; a bare Enter is "No, exit".
func TestTrustPromptModeWaitsForDownThenEnter(t *testing.T) {
	e := setup(t)
	c := e.cmd(t, []string{"FAKECLAUDE_TRUST_PROMPT=1"}, "--session-id", sid)
	stdin, _ := c.StdinPipe()
	var out lockedString
	c.Stdout, c.Stderr = &out, os.Stderr
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { c.Process.Kill(); c.Wait() }()
	pid := c.Process.Pid
	waitFor(t, "the trust dialog", func() bool { return strings.Contains(out.String(), "Quick safety check") })
	if _, ok := registry(e, pid); ok {
		t.Fatal("a Claude waiting at the trust dialog must have no registry entry")
	}
	// The cursor-down sequence real tmux sends, then Enter.
	io.WriteString(stdin, "\x1b[B\r")
	waitFor(t, "registry idle", func() bool { r, ok := registry(e, pid); return ok && r.Status == "idle" })
	if !strings.Contains(out.String(), "Yes, I trust this folder") {
		t.Errorf("output should show the answered dialog:\n%s", out.String())
	}
	io.WriteString(stdin, "/exit\n")
	if err := c.Wait(); err != nil {
		t.Fatalf("exit: %v", err)
	}

	// Enter alone selects "No, exit": non-zero, no registry, no session.
	c2 := e.cmd(t, []string{"FAKECLAUDE_TRUST_PROMPT=1"}, "--session-id", sid)
	stdin2, _ := c2.StdinPipe()
	c2.Stdout, c2.Stderr = io.Discard, os.Stderr
	if err := c2.Start(); err != nil {
		t.Fatal(err)
	}
	io.WriteString(stdin2, "\r")
	if err := c2.Wait(); err == nil {
		t.Error("declining the trust dialog must exit non-zero")
	}
	if _, ok := registry(e, c2.Process.Pid); ok {
		t.Error("declining the trust dialog must leave no registry entry")
	}
}

// lockedString is a concurrency-safe io.Writer for a child's stdout.
type lockedString struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedString) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedString) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
