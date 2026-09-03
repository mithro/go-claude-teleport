// internal/orchestrate/e2e_test.go
package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
)

// teleport runs preflight + all steps in-process and returns the journal.
func teleport(t *testing.T, o Options, src, dst *host) (*Plan, *job.Journal) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	driver := src
	if o.Direction == "from" {
		driver = dst
	}
	p, err := Preflight(ctx, o, src.ep, dst.ep, sid)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	j, err := job.New(driver.paths.DataDir, sid)
	if err != nil {
		t.Fatal(err)
	}
	j.Plan, _ = p.ToJSON()
	j.Save()
	factory := func(context.Context, Options) (remote.Endpoint, remote.Endpoint, func(), error) {
		return src.ep, dst.ep, func() {}, nil
	}
	if err := RunJob(ctx, driver.paths.DataDir, sid, factory, t.Logf); err != nil {
		t.Fatalf("run: %v", err)
	}
	jj, _, _ := job.Open(driver.paths.DataDir, sid)
	p, _ = PlanFromJournal(jj)
	return p, jj
}

func readTranscript(t *testing.T, h *host, cwd string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(h.paths.ProjectDir(cwd), sid+".jsonl"))
	if err != nil {
		t.Fatalf("transcript on %s: %v", h.name, err)
	}
	return string(b)
}

func TestE2ERunningWorktreeFreshMain(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", newFakeTmux())
	dst := newHost(t, "big-storage.example", "bob", newFakeTmux())
	main := filepath.Join(src.paths.Home, "github", "x")
	makeRepo(t, main)
	w := filepath.Join(main, ".worktrees", "feat")
	gitc(t, main, "worktree", "add", "-q", "-b", "feat", w)
	os.WriteFile(filepath.Join(w, "wip.go"), []byte("package wip\n"), 0o644)
	seedSession(t, src, w)
	startClaudeInPane(t, src, "work", w)

	o := baseOptions()
	p, j := teleport(t, o, src, dst)
	if j.Outcome != "success" {
		t.Fatalf("outcome %q", j.Outcome)
	}
	// Destination: transcript rewritten, worktree attached, Claude running in group "work".
	dstW := filepath.Join(dst.paths.Home, "github", "x", ".worktrees", "feat")
	tr := readTranscript(t, dst, dstW)
	if strings.Contains(tr, src.paths.Home) || !strings.Contains(tr, dstW) {
		t.Errorf("destination transcript not rewritten:\n%s", tr)
	}
	if diff := cmp.Diff(strings.TrimSpace(gitc(t, w, "status", "--porcelain")), strings.TrimSpace(gitc(t, dstW, "status", "--porcelain"))); diff != "" {
		t.Errorf("git status differs (-src +dst):\n%s", diff)
	}
	reg := waitRegistry(t, dst, "idle")
	if !strings.HasPrefix(reg.Tmux, "work:") || reg.Cwd != dstW {
		t.Errorf("dest registry = %+v", reg)
	}
	if p.Git.Mode != gitx.ModeFreshMain || !p.CreatedSession || !p.CreatedWindow {
		t.Errorf("plan = mode %s created %v/%v", p.Git.Mode, p.CreatedSession, p.CreatedWindow)
	}
	// Source: Claude exited, pane shows the placeholder with --teleported-to.
	if _, ok, _ := src.ep.ClaudeStatus(context.Background(), session.ID(sid)); ok {
		t.Error("source claude still running")
	}
	argv := waitPlaceholderForeground(t, src, p.Session.Tmux)
	if id, ok := procx.IsPlaceholderArgv(argv); !ok || id != sid || !contains(argv, "--teleported-to") {
		t.Errorf("source pane argv = %v", argv)
	}
	// Source files untouched; history recorded on both; staging gone.
	if _, err := os.Stat(filepath.Join(src.paths.ProjectDir(w), sid+".jsonl")); err != nil {
		t.Error("source transcript removed")
	}
	for _, h := range []*host{src, dst} {
		if _, err := os.Stat(filepath.Join(job.Dir(h.paths.DataDir, sid), "history.jsonl")); err != nil {
			t.Errorf("history missing on %s", h.name)
		}
	}
	if _, err := os.Stat(job.StagingDir(dst.paths.DataDir, sid)); !os.IsNotExist(err) {
		t.Error("staging not cleaned up")
	}
}

func TestE2EFromDirection(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", newFakeTmux())
	dst := newHost(t, "big-storage.example", "bob", newFakeTmux())
	cwd := filepath.Join(src.paths.Home, "proj")
	makeRepo(t, cwd)
	seedSession(t, src, cwd)
	startClaudeInPane(t, src, "main", cwd)
	o := baseOptions()
	o.Direction, o.Target = "from", "alice@laptop.example"
	_, j := teleport(t, o, src, dst)
	if j.Outcome != "success" || j.Dir != job.Dir(dst.paths.DataDir, sid) {
		t.Fatalf("outcome %q dir %s", j.Outcome, j.Dir)
	}
	waitRegistry(t, dst, "idle")
}

func TestE2EMainCheckoutExistingMainFastForward(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", newFakeTmux())
	dst := newHost(t, "big-storage.example", "bob", newFakeTmux())
	cwd := filepath.Join(src.paths.Home, "proj")
	makeRepo(t, cwd)
	dstRepo := filepath.Join(dst.paths.Home, "proj")
	gitc(t, filepath.Dir(dstRepo), "clone", "-q", cwd, dstRepo)
	gitc(t, dstRepo, "remote", "remove", "origin")
	os.WriteFile(filepath.Join(cwd, "more.txt"), []byte("more\n"), 0o644)
	gitc(t, cwd, "add", ".")
	gitc(t, cwd, "commit", "-q", "-m", "more")
	tip := strings.TrimSpace(gitc(t, cwd, "rev-parse", "HEAD"))
	os.WriteFile(filepath.Join(cwd, "untracked.txt"), []byte("u\n"), 0o644)
	seedSession(t, src, cwd)
	startClaudeInPane(t, src, "main", cwd)
	p, j := teleport(t, baseOptions(), src, dst)
	if j.Outcome != "success" || p.Git.Mode != gitx.ModeExistingMain || !p.Git.FastForward {
		t.Fatalf("outcome %q plan %+v", j.Outcome, p.Git)
	}
	if got := strings.TrimSpace(gitc(t, dstRepo, "rev-parse", "HEAD")); got != tip {
		t.Errorf("dest HEAD %s, want %s", got, tip)
	}
	if got := gitc(t, dstRepo, "status", "--porcelain"); !strings.Contains(got, "?? untracked.txt") {
		t.Errorf("dest status:\n%s", got)
	}
}

func TestE2ESuspendedSourceStaysSuspended(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", newFakeTmux())
	dst := newHost(t, "big-storage.example", "bob", newFakeTmux())
	cwd := filepath.Join(src.paths.Home, "proj")
	seedSession(t, src, cwd)
	ctx := context.Background()
	refLine, _ := src.tmux.Run(ctx, `new-session -d -s "work" -n "claude" -c "`+cwd+`" -P -F "#{pane_id}	#{window_id}	#{session_name}"`)
	parts := strings.Split(refLine[0], "\t")
	paneID, windowID := parts[0], parts[1]
	if _, err := src.tmux.Run(ctx, `send-keys -t "`+paneID+`" " claude-teleport placeholder --resume `+sid+`" Enter`); err != nil {
		t.Fatal(err)
	}
	// waitPlaceholderForeground, not a fixed sleep: fork+exec of the typed
	// command races against this goroutine, and a real pty (needed so the
	// placeholder's own stdin-is-a-terminal check sees what a real tmux
	// pane would, and so it waits for Enter instead of auto-resuming) adds
	// enough unpredictable setup latency that a fixed sleep is flaky.
	tmuxRef := &session.TmuxRef{SocketPath: src.tmux.socket, Session: "work", WindowID: windowID, PaneID: paneID}
	waitPlaceholderForeground(t, src, tmuxRef)
	o := baseOptions()
	o.Selector = session.Selector{TmuxSess: "work", TmuxWindow: "claude"}
	p, j := teleport(t, o, src, dst)
	if j.Outcome != "success" || p.Session.State != session.StateSuspended || p.TargetState != "suspended" {
		t.Fatalf("outcome %q state %s target %s", j.Outcome, p.Session.State, p.TargetState)
	}
	waitPlaceholderForeground(t, dst, p.DestRef)
	if _, ok, _ := dst.ep.ClaudeStatus(ctx, session.ID(sid)); ok {
		t.Error("dest claude should have exited for the suspended state")
	}
}

func TestE2EIdleNoTmuxDestination(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", newFakeTmux())
	dst := newHost(t, "big-storage.example", "bob", nil)
	cwd := filepath.Join(src.paths.Home, "proj")
	seedSession(t, src, cwd)
	startClaudeInPane(t, src, "main", cwd)
	o := baseOptions()
	o.State = "idle"
	p, j := teleport(t, o, src, dst)
	if j.Outcome != "success" || p.Tmux != nil {
		t.Fatalf("outcome %q tmux %+v", j.Outcome, p.Tmux)
	}
	readTranscript(t, dst, filepath.Join(dst.paths.Home, "proj"))
	if _, ok, _ := dst.ep.ClaudeStatus(context.Background(), session.ID(sid)); ok {
		t.Error("dest claude must not be running after the pty confirmation")
	}
}

func TestE2EReTeleportBackFastForwardsTranscript(t *testing.T) {
	a := newHost(t, "laptop.example", "alice", newFakeTmux())
	b := newHost(t, "big-storage.example", "bob", newFakeTmux())
	cwd := filepath.Join(a.paths.Home, "proj")
	seedSession(t, a, cwd)
	startClaudeInPane(t, a, "main", cwd)
	if _, j := teleport(t, baseOptions(), a, b); j.Outcome != "success" {
		t.Fatal("first teleport failed")
	}
	// Work happens on b: the transcript grows.
	bCwd := filepath.Join(b.paths.Home, "proj")
	bTr := filepath.Join(b.paths.ProjectDir(bCwd), sid+".jsonl")
	f, _ := os.OpenFile(bTr, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"type":"user","sessionId":"` + sid + `","cwd":"` + bCwd + `","timestamp":"2026-08-27T11:00:00Z","message":{"role":"user","content":"more work"}}` + "\n")
	f.Close()
	// Teleport back: a already has an older copy -> fast-forward.
	o := baseOptions()
	o.Target = "alice@laptop.example"
	b.refreshProbe(t)
	p, j := teleport(t, o, b, a)
	if j.Outcome != "success" {
		t.Fatalf("teleport back: %q", j.Outcome)
	}
	if !strings.Contains(readTranscript(t, a, cwd), "more work") {
		t.Error("source transcript was not fast-forwarded")
	}
	_ = p
}
