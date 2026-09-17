package session

import (
	"os"
	"path/filepath"
	"testing"
)

// A project directory reached by two names is ONE directory. Claude Code
// files a transcript under projects/Munge(cwd); when a session's work
// moves to another repository the user can symlink the new munged name at
// the old directory, so the same transcript keeps being appended to and
// stays reachable from both cwds.
//
// filepath.Glob expands path components textually and never resolves a
// symlink, so the glob returns the file twice -- and a count of the hits
// called that an ambiguous session and refused.
//
// Real case (2026-09-17, x1c-work): session 1d7814e6 launched in
// wafer-space/tinytapeout-boards and now works in
// fpgas-online/fpgas.online-mechanical;
// projects/-home-tim-github-fpgas-online-fpgas-online-mechanical is a
// symlink to projects/-home-tim-github-wafer-space-tinytapeout-boards, and
// both spellings name inode 11159503. Teleporting it failed with "session
// ... has 2 transcripts under /home/tim/.claude/projects".
func TestFindTranscriptThroughASymlinkedProjectDir(t *testing.T) {
	home := t.TempDir()
	p := NewPaths(home, filepath.Join(home, ".claude"), "/proc", true)
	real := filepath.Join(p.ProjectsDir(), "-home-tim-real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	id := ID("3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c")
	want := filepath.Join(real, string(id)+".jsonl")
	if err := os.WriteFile(want, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(p.ProjectsDir(), "-home-tim-alias")); err != nil {
		t.Fatal(err)
	}

	got, err := FindTranscript(p.ProjectsDir(), id)
	if err != nil {
		t.Fatalf("FindTranscript: %v", err)
	}
	fi, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	wf, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(fi, wf) {
		t.Errorf("FindTranscript = %q, which is not the transcript at %q", got, want)
	}
}

// Two genuinely different files with the same session id really are
// ambiguous, and must still be refused: collapsing them would pick one
// session's history at random.
func TestFindTranscriptStillRejectsTwoDistinctTranscripts(t *testing.T) {
	home := t.TempDir()
	p := NewPaths(home, filepath.Join(home, ".claude"), "/proc", true)
	id := ID("3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c")
	for i, name := range []string{"-home-tim-one", "-home-tim-two"} {
		dir := filepath.Join(p.ProjectsDir(), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := []byte{'{', '}', byte('0' + i), '\n'} // distinct content
		if err := os.WriteFile(filepath.Join(dir, string(id)+".jsonl"), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := FindTranscript(p.ProjectsDir(), id); err == nil {
		t.Fatal("two distinct transcripts for one id must be an error")
	}
}

// With both spellings naming the same file, WHICH one becomes ProjectDir
// decides where the session is rooted: ProjectCwd reads it back, and the
// destination cwd, the repository transferred and the directory the
// resumed Claude is launched in all follow from that.
//
// The work cwd is where the session actually is -- its pane is there and
// Claude Code is appending there -- so that spelling wins. Rooting at the
// launch cwd would move the repository the session left.
func TestLoadPrefersTheProjectDirSpellingMatchingWorkCwd(t *testing.T) {
	launch := "/home/tim/github/wafer-space/tinytapeout-boards"
	work := "/home/tim/github/fpgas-online/fpgas.online-mechanical"

	home := t.TempDir()
	p := NewPaths(home, filepath.Join(home, ".claude"), "/proc", true)
	real := filepath.Join(p.ProjectsDir(), Munge(launch))
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	id := ID("3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c")
	var body string
	for _, cwd := range []string{launch, work} {
		body += `{"type":"user","sessionId":"` + string(id) + `","cwd":"` + cwd +
			`","gitBranch":"main","version":"2.1.251"}` + "\n"
	}
	if err := os.WriteFile(filepath.Join(real, string(id)+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(p.ProjectsDir(), Munge(work))); err != nil {
		t.Fatal(err)
	}

	s, err := Load(p, id, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := filepath.Base(s.ProjectDir), Munge(work); got != want {
		t.Errorf("ProjectDir base = %q, want %q (the work cwd's spelling)", got, want)
	}
	if got := s.ProjectCwd(); got != work {
		t.Errorf("ProjectCwd = %q, want %q", got, work)
	}
}

// The ordinary case -- one spelling, no symlink -- must be untouched: a
// session that never left its launch cwd is still rooted there.
func TestLoadUnchangedWithASingleSpelling(t *testing.T) {
	launch := "/home/tim/github/mithro/esp32-to-433mhz"
	s := writeSession(t, Munge(launch), launch, launch)
	if got := s.ProjectCwd(); got != launch {
		t.Errorf("ProjectCwd = %q, want %q", got, launch)
	}
	if got, want := filepath.Base(s.ProjectDir), Munge(launch); got != want {
		t.Errorf("ProjectDir base = %q, want %q", got, want)
	}
}
