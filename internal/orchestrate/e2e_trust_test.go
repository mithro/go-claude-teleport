// internal/orchestrate/e2e_trust_test.go
package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
)

// runJob runs the job in dir as the detached runner would, returning its
// error instead of failing the test (teleport() insists on success).
func runJob(t *testing.T, dataDir string, src, dst *host) (*job.Journal, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	factory := func(context.Context, Options) (remote.Endpoint, remote.Endpoint, func(), error) {
		return src.ep, dst.ep, func() {}, nil
	}
	err := RunJob(ctx, dataDir, sid, factory, t.Logf)
	j, _, oerr := job.Open(dataDir, sid)
	if oerr != nil {
		t.Fatal(oerr)
	}
	return j, err
}

// TestE2ETrustPromptFailsThenContinueAfterAManualAnswer is the end-to-end
// shape of the first real teleport's dead end (ruling R-P3-TRUST-1 items 2
// and 3), with a real fakeclaude sitting at the trust dialog on the
// destination:
//
//	attempt 1: everything installs, the destination Claude resumes and
//	           stops at the dialog, so no registry entry appears — with no
//	           trust to carry, start fails saying exactly that, and the
//	           source is thawed and kept.
//	between:   a human answers the dialog in that pane and the session
//	           lives on the DESTINATION, which then appends to its own copy
//	           of the transcript.
//	continue:  the job completes without ever re-capturing, re-sending or
//	           re-installing those session files — the destination's newer
//	           transcript survives untouched, where the old behaviour
//	           re-sent the source's copy and had install refuse the
//	           divergence for ever.
func TestE2ETrustPromptFailsThenContinueAfterAManualAnswer(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", newFakeTmux())
	dst := newHostEnv(t, "big-storage.example", "bob", newFakeTmux(), "FAKECLAUDE_TRUST_PROMPT=1")
	cwd := filepath.Join(src.paths.Home, "proj")
	makeRepo(t, cwd)
	seedSession(t, src, cwd)
	startClaudeInPane(t, src, "main", cwd)

	ctx := context.Background()
	o := baseOptions()
	o.StartTimeout = 5 * time.Second
	p, err := Preflight(ctx, o, src.ep, dst.ep, sid)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if p.sourceTrusted() {
		t.Fatal("this source has no project entry at all: it must not claim trust")
	}
	j, err := job.New(src.paths.DataDir, sid)
	if err != nil {
		t.Fatal(err)
	}
	j.Plan, _ = p.ToJSON()
	if err := j.Save(); err != nil {
		t.Fatal(err)
	}

	// Attempt 1: fails at start, with the trust advice and no /login.
	j, runErr := runJob(t, src.paths.DataDir, src, dst)
	if runErr == nil {
		t.Fatal("attempt 1 must fail: the destination Claude is waiting at its trust dialog")
	}
	if name, _ := FailedStep(j); name != "start" {
		t.Fatalf("failed at step %q, want start", name)
	}
	if got := j.Step("start").Error; !strings.Contains(got, remote.TrustPromptWaiting) || !strings.Contains(got, "claude-teleport continue "+sid) {
		t.Errorf("start error = %q, want the trust-prompt advice", got)
	}
	if strings.Contains(j.Step("start").Error, "/login") {
		t.Errorf("start error blames login: %q", j.Step("start").Error)
	}
	if _, ok, _ := src.ep.ClaudeStatus(ctx, session.ID(sid)); !ok {
		t.Fatal("the source session must be kept (thawed and running) when the destination is not confirmed")
	}
	p1, err := PlanFromJournal(j)
	if err != nil {
		t.Fatal(err)
	}
	if p1.DestRef == nil {
		t.Fatal("the start step opened no destination pane")
	}

	// A human answers the dialog in that pane, and the destination Claude
	// then does a turn of its own — the transcript there is now ahead of
	// the source's copy.
	if err := tmuxx.SendKeys(ctx, dst.tmux, p1.DestRef.PaneID, "Down"); err != nil {
		t.Fatal(err)
	}
	if err := tmuxx.SendKeys(ctx, dst.tmux, p1.DestRef.PaneID, "Enter"); err != nil {
		t.Fatal(err)
	}
	waitRegistry(t, dst, "idle")
	if err := tmuxx.SendKeys(ctx, dst.tmux, p1.DestRef.PaneID, "a turn typed on the destination", "Enter"); err != nil {
		t.Fatal(err)
	}
	dstTranscript := filepath.Join(dst.paths.ProjectDir(dst.paths.Home+"/proj"), sid+".jsonl")
	waitFileContains(t, dstTranscript, "a turn typed on the destination")

	// Continue: completes, and never touches the destination's own copy.
	j, runErr = runJob(t, src.paths.DataDir, src, dst)
	if runErr != nil {
		t.Fatalf("continue: %v", runErr)
	}
	if j.Outcome != "success" {
		t.Fatalf("outcome %q", j.Outcome)
	}
	if got := readFile(t, dstTranscript); !strings.Contains(got, "a turn typed on the destination") {
		t.Error("the destination's own transcript was overwritten by the source's stale copy")
	}
	// The source is released exactly as any successful teleport releases
	// it: Claude gone, the pane showing the placeholder.
	if _, ok, _ := src.ep.ClaudeStatus(ctx, session.ID(sid)); ok {
		t.Error("source claude still running after a successful continue")
	}
	argv := waitPlaceholderForeground(t, src, p.Session.Tmux)
	if id, ok := procx.IsPlaceholderArgv(argv); !ok || id != sid {
		t.Errorf("source pane argv = %v", argv)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func waitFileContains(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never contained %q", path, want)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
