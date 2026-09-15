package session

import (
	"os"
	"path/filepath"
	"testing"
)

// writeSession lays a transcript down in a named project directory with
// the given cwd records, and loads it.
func writeSession(t *testing.T, projectDirName string, cwds ...string) *Session {
	t.Helper()
	home := t.TempDir()
	p := NewPaths(home, filepath.Join(home, ".claude"), "/proc", true)
	dir := filepath.Join(p.ProjectsDir(), projectDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := ID("3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c")
	var body string
	for _, cwd := range cwds {
		body += `{"type":"user","sessionId":"` + string(id) + `","cwd":"` + cwd +
			`","gitBranch":"main","version":"2.1.251"}` + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, string(id)+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(p, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A session that changed directory records two cwds, and Claude Code files
// the transcript under only ONE of them. Everything that does
// project-directory arithmetic has to use that one.
//
// Observed on ten64 (2026-09-15): 11 of 15 sessions had launch_cwd !=
// work_cwd -- claude started in a container directory, the work happened
// in a git worktree several levels down, and the transcript was filed
// under the worktree. Using LaunchCwd there names a project directory that
// does not exist, which is what refused the teleport.
func TestProjectCwdFollowsTheTranscript(t *testing.T) {
	launch := "/home/tim/github/fpgas-online"
	work := "/home/tim/github/fpgas-online/fpgas.online-infra/.claude/worktrees/configtxt"

	s := writeSession(t, Munge(work), launch, work)
	if s.LaunchCwd != launch || s.WorkCwd != work {
		t.Fatalf("fixture: launch=%q work=%q", s.LaunchCwd, s.WorkCwd)
	}
	if got := s.ProjectCwd(); got != work {
		t.Errorf("ProjectCwd = %q, want the work cwd %q (the transcript is filed there)", got, work)
	}
	// The whole point: Munge(ProjectCwd) must name the real directory.
	if got := Munge(s.ProjectCwd()); got != filepath.Base(s.ProjectDir) {
		t.Errorf("Munge(ProjectCwd) = %q, want %q", got, filepath.Base(s.ProjectDir))
	}
}

// The ordinary session -- never left its directory -- is unchanged.
func TestProjectCwdIsLaunchCwdWhenTheyAgree(t *testing.T) {
	cwd := "/home/alice/github/widget"
	s := writeSession(t, Munge(cwd), cwd)
	if got := s.ProjectCwd(); got != cwd {
		t.Errorf("ProjectCwd = %q, want %q", got, cwd)
	}
}

// A session filed under its LAUNCH cwd despite having moved on keeps using
// the launch cwd: the transcript's location decides, not a preference for
// one field over the other.
func TestProjectCwdPrefersWhicheverMatches(t *testing.T) {
	launch := "/home/alice/github/widget"
	work := "/home/alice/github/widget/sub"
	s := writeSession(t, Munge(launch), launch, work)
	if got := s.ProjectCwd(); got != launch {
		t.Errorf("ProjectCwd = %q, want the launch cwd %q, which is where the transcript is", got, launch)
	}
}

// Neither matches (a renamed or hand-moved project directory): fall back to
// LaunchCwd, which is what every caller used before ProjectCwd existed, so
// an unrecognised layout behaves exactly as it does today.
func TestProjectCwdFallsBackToLaunchCwd(t *testing.T) {
	launch := "/home/alice/github/widget"
	work := "/home/alice/github/widget/sub"
	s := writeSession(t, "-some-hand-renamed-directory", launch, work)
	if got := s.ProjectCwd(); got != launch {
		t.Errorf("ProjectCwd = %q, want the launch-cwd fallback %q", got, launch)
	}
}
