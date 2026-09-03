package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// seedTranscript writes the minimum session.Load needs for cwd: a
// transcript under the munged project dir for sid.
func seedTranscript(t *testing.T, p session.Paths, cwd string) {
	t.Helper()
	proj := p.ProjectDir(cwd)
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(`{"cwd":"`+cwd+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestSessionExtrasFindsTrustAtTheMainRepoForALinkedWorktree is ruling
// R-P3-TRUST-1 item 1. Real Claude Code 2.1.259 records the first-run
// trust dialog for a session running in a LINKED git worktree under the
// project entry keyed by the MAIN repository path — the worktree cwd gets
// no entry at all (observed on the machine that ran the first real
// teleport: ~/tmp-teleport-proof was keyed and trusted,
// ~/tmp-teleport-proof/.worktrees/x was absent). SessionExtras must
// therefore look past the cwd, and must tell the destination WHERE to
// grant the trust in ITS OWN paths (the mapped main repo).
func TestSessionExtrasFindsTrustAtTheMainRepoForALinkedWorktree(t *testing.T) {
	p := testPaths(t)
	main := filepath.Join(p.Home, "repo")
	os.MkdirAll(main, 0o755)
	gitc(t, main, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(main, "a"), []byte("a"), 0o644)
	gitc(t, main, "add", "a")
	gitc(t, main, "commit", "-q", "-m", "i")
	wt := filepath.Join(main, ".worktrees", "x")
	gitc(t, main, "worktree", "add", "-q", "-b", "feature", wt)
	seedTranscript(t, p, wt)
	// Exactly what the real host had: the main repo trusted, no entry at
	// all for the worktree the session runs in.
	os.WriteFile(p.GlobalJSON, []byte(`{"projects":{"`+main+`":{"hasTrustDialogAccepted":true,"allowedTools":[]}}}`), 0o600)

	l := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc", Logf: t.Logf})
	pm := session.NewPathMap(session.Mapping{From: p.Home, To: "/home/bob"})
	ex, err := l.SessionExtras(context.Background(), session.ID(sid), pm)
	if err != nil {
		t.Fatal(err)
	}
	if !ex.SourceTrusted {
		t.Error("SourceTrusted = false: the main repo's accepted trust dialog must be found for a linked-worktree session")
	}
	want := "/home/bob/repo"
	if ex.TrustCwd != want {
		t.Errorf("TrustCwd = %q, want the mapped main repo %q", ex.TrustCwd, want)
	}
	// The cwd's own (absent) entry is still absent: nothing is invented.
	if ex.ProjectEntry != nil {
		t.Errorf("ProjectEntry = %v, want nil for a cwd with no entry", ex.ProjectEntry)
	}
}

// TestSessionExtrasPrefersTheCwdsOwnProjectEntry covers the ordinary case
// (and the same worktree layout, to prove the main-repo fallback does not
// override a cwd that has its own entry): trust is read from the cwd, and
// TrustCwd is the mapped cwd.
func TestSessionExtrasPrefersTheCwdsOwnProjectEntry(t *testing.T) {
	p := testPaths(t)
	cwd := filepath.Join(p.Home, "proj")
	seedTranscript(t, p, cwd)
	os.WriteFile(p.GlobalJSON, []byte(`{"projects":{"`+cwd+`":{"hasTrustDialogAccepted":true}}}`), 0o600)
	l := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc", Logf: t.Logf})
	pm := session.NewPathMap(session.Mapping{From: p.Home, To: "/home/bob"})
	ex, err := l.SessionExtras(context.Background(), session.ID(sid), pm)
	if err != nil {
		t.Fatal(err)
	}
	if !ex.SourceTrusted || ex.TrustCwd != "/home/bob/proj" {
		t.Errorf("SourceTrusted=%v TrustCwd=%q, want true and the mapped cwd", ex.SourceTrusted, ex.TrustCwd)
	}
	if !session.TrustAccepted(ex.ProjectEntry) {
		t.Errorf("ProjectEntry = %v", ex.ProjectEntry)
	}
}

// TestSessionExtrasReportsNoTrustWhenNeitherPathHasIt pins the negative:
// an untrusted source must not claim trust the destination would then
// auto-accept a dialog for.
func TestSessionExtrasReportsNoTrustWhenNeitherPathHasIt(t *testing.T) {
	p := testPaths(t)
	cwd := filepath.Join(p.Home, "proj")
	seedTranscript(t, p, cwd)
	os.WriteFile(p.GlobalJSON, []byte(`{"projects":{"`+cwd+`":{"hasTrustDialogAccepted":false}}}`), 0o600)
	l := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc", Logf: t.Logf})
	ex, err := l.SessionExtras(context.Background(), session.ID(sid), session.PathMap{})
	if err != nil {
		t.Fatal(err)
	}
	if ex.SourceTrusted {
		t.Error("SourceTrusted = true for a cwd whose entry says hasTrustDialogAccepted false")
	}
}
