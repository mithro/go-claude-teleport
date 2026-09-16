package placeholder

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// `claude --resume` is project-scoped: it resolves the id against the
// project matching the CURRENT directory's munged name, so the placeholder
// has to cd to the directory the transcript is really filed under.
//
// For a session that changed directory that is the WORK cwd, not the
// launch cwd. ChdirTarget applied the right rule -- Munge(cwd) must name
// the transcript's project directory -- but only ever offered it the
// launch cwd, so such a session failed the check, chdir came back empty
// and the placeholder resumed from wherever the pane happened to be, where
// the id does not resolve.
//
// Real case (2026-09-17): session 1d7814e6, whose pane shell sits in
// wafer-space/tinytapeout-boards while the transcript is filed under
// fpgas-online/fpgas.online-mechanical.
func TestChdirTargetFallsBackToTheWorkCwd(t *testing.T) {
	root := t.TempDir()
	launch := filepath.Join(root, "launched-here")
	work := filepath.Join(root, "works-here")
	for _, d := range []string{launch, work} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Filed under the WORK cwd, as Claude Code files it.
	transcript := filepath.Join(root, "projects", session.Munge(work), sid+".jsonl")

	got := ChdirTarget(session.Meta{LaunchCwd: launch, WorkCwd: work}, transcript)
	if got != work {
		t.Errorf("ChdirTarget = %q, want the work cwd %q", got, work)
	}
}

// The launch cwd still wins when it is the one the transcript is filed
// under, even if the session later moved somewhere that also exists.
func TestChdirTargetPrefersTheLaunchCwdWhenItMatches(t *testing.T) {
	root := t.TempDir()
	launch := filepath.Join(root, "launched-here")
	work := filepath.Join(root, "works-here")
	for _, d := range []string{launch, work} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	transcript := filepath.Join(root, "projects", session.Munge(launch), sid+".jsonl")

	if got := ChdirTarget(session.Meta{LaunchCwd: launch, WorkCwd: work}, transcript); got != launch {
		t.Errorf("ChdirTarget = %q, want the launch cwd %q", got, launch)
	}
}

// A work cwd that matches the project directory but no longer exists is
// still refused: cd'ing nowhere is worse than resuming in place.
func TestChdirTargetRefusesAMissingWorkCwd(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "gone")
	transcript := filepath.Join(root, "projects", session.Munge(work), sid+".jsonl")

	if got := ChdirTarget(session.Meta{LaunchCwd: filepath.Join(root, "also-gone"), WorkCwd: work}, transcript); got != "" {
		t.Errorf("ChdirTarget = %q, want \"\" for a directory that does not exist", got)
	}
}
