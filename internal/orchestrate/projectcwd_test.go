package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/gitx"
)

// writeTranscript files a transcript in the project directory belonging to
// projectCwd, recording launch as the first cwd and work as the last. That
// is the shape seedSession cannot make: a session that changed directory,
// where Claude Code filed the transcript under the directory it ended up
// in rather than the one it started in.
func writeTranscript(t *testing.T, h *host, projectCwd, launch, work, branch string) {
	t.Helper()
	dir := h.paths.ProjectDir(projectCwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := ""
	for _, cwd := range []string{launch, work} {
		body += `{"type":"user","sessionId":"` + sid + `","cwd":"` + cwd +
			`","gitBranch":"` + branch + `","version":"2.1.251"}` + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// onDst maps a source path through the home rewrite the two fixture hosts
// always imply, so a test can name the destination path it expects.
func onDst(src, dst *host, path string) string {
	return strings.Replace(path, src.paths.Home, dst.paths.Home, 1)
}

// Shape 3, seen on ten64: claude was started in a CONTAINER directory that
// is not a git repository and holds many repos, then worked in one of
// them. Rooting the transfer at the launch cwd walked the whole container
// tree (491,141 entries on the real machine) and then refused, because the
// destination cannot corroborate a pre-existing root against a transcript
// filed somewhere else entirely.
//
// Rooting at the cwd the transcript actually belongs to makes it an
// ordinary repository transfer.
func TestPreflightRootsAtTheProjectCwdNotTheLaunchCwd(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "alice", nil)

	container := filepath.Join(src.paths.Home, "github", "org") // not a repo
	work := filepath.Join(container, "widget")                  // a repo inside it
	makeRepo(t, work)
	if err := os.MkdirAll(filepath.Join(container, "other-repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, src, work, container, work, "main")

	o := baseOptions()
	o.State = "idle"
	o.NoTmux = true
	o.AllowDrift = true
	p, err := Preflight(context.Background(), o, src.ep, dst.ep, sid)
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if want := onDst(src, dst, work); p.DestCwd != want {
		t.Errorf("DestCwd = %q, want the project cwd %q, not the launch cwd %q",
			p.DestCwd, want, container)
	}
	if p.Git == nil || p.Git.Mode == gitx.ModeNotRepo {
		t.Fatalf("git mode = %v, want a repository mode: rooting at %q is what made it "+
			"not-a-repo and dragged the whole container tree in", p.Git, container)
	}
	if p.Git.SrcWorktree != work {
		t.Errorf("SrcWorktree = %q, want %q", p.Git.SrcWorktree, work)
	}
}

// Shape 2: claude was started in the MAIN repo and worked in one of its
// linked worktrees. gitx deliberately skips other linked worktrees, so
// rooting at the launch cwd transferred the main checkout and silently
// dropped the directory the session actually works in -- the session would
// resume pointing at a path that does not exist (issue #22).
func TestPreflightCarriesTheWorktreeTheSessionWorksIn(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "alice", nil)

	main := filepath.Join(src.paths.Home, "github", "widget")
	makeRepo(t, main)
	work := filepath.Join(main, ".claude", "worktrees", "feature")
	gitc(t, main, "worktree", "add", "-q", "-b", "feature", work)
	writeTranscript(t, src, work, main, work, "feature")

	o := baseOptions()
	o.State = "idle"
	o.NoTmux = true
	o.AllowDrift = true
	p, err := Preflight(context.Background(), o, src.ep, dst.ep, sid)
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if want := onDst(src, dst, work); p.DestCwd != want {
		t.Errorf("DestCwd = %q, want the worktree %q the session works in", p.DestCwd, want)
	}
	if p.Git == nil || p.Git.SrcWorktree != work {
		t.Fatalf("SrcWorktree = %v, want the linked worktree %q -- otherwise the "+
			"session resumes into a directory that was never transferred", p.Git, work)
	}
}

// The ordinary session, which never left its directory, must be untouched
// by all of this.
func TestPreflightUnchangedWhenLaunchAndWorkAgree(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "alice", nil)

	cwd := filepath.Join(src.paths.Home, "github", "widget")
	makeRepo(t, cwd)
	writeTranscript(t, src, cwd, cwd, cwd, "main")

	o := baseOptions()
	o.State = "idle"
	o.NoTmux = true
	o.AllowDrift = true
	p, err := Preflight(context.Background(), o, src.ep, dst.ep, sid)
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if want := onDst(src, dst, cwd); p.DestCwd != want {
		t.Errorf("DestCwd = %q, want %q", p.DestCwd, want)
	}
	if p.Git == nil || p.Git.SrcWorktree != cwd {
		t.Errorf("SrcWorktree = %v, want %q", p.Git, cwd)
	}
}
