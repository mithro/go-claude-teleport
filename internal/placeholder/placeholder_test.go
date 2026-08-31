package placeholder

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

const sid = "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13"

// fixture builds a projects dir whose transcript's launch cwd is a real
// temp directory, so ChdirTarget has something to stat.
func fixture(t *testing.T) (projectsDir, launchCwd string) {
	t.Helper()
	root := t.TempDir()
	launchCwd = filepath.Join(root, "work")
	os.MkdirAll(launchCwd, 0o700)
	projectsDir = filepath.Join(root, "projects")
	proj := filepath.Join(projectsDir, session.Munge(launchCwd))
	os.MkdirAll(proj, 0o700)
	rec := `{"type":"user","cwd":"` + launchCwd + `","gitBranch":"feature/teleport","sessionId":"` + sid + `","timestamp":"2026-08-27T10:15:30.123Z","message":{"content":"Add a --verbose flag"}}` + "\n" +
		`{"type":"custom-title","customTitle":"widget-verbose"}` + "\n"
	os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(rec), 0o600)
	return projectsDir, launchCwd
}

func TestDecideResumesOnEnter(t *testing.T) {
	projects, cwd := fixture(t)
	var out bytes.Buffer
	d := Decide(&out, Options{SessionID: sid, ProjectsDir: projects, Home: filepath.Dir(cwd)}, false, true,
		func() (string, error) { return "\n", nil })
	if d.Skip || d.Chdir != cwd || strings.Join(d.Argv, " ") != "claude --resume "+sid {
		t.Fatalf("%+v", d)
	}
	s := out.String()
	for _, w := range []string{"Resume Claude session", "3f9c2b7e", "~/work", "feature/teleport", "widget-verbose", "last active 2026-08-27T10:15:30.123Z", "Enter = resume", "resuming"} {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in:\n%s", w, s)
		}
	}
	if strings.Contains(s, "\033[") {
		t.Error("no ANSI when stdout is not a tty")
	}
}

func TestDecideSkipsOnInterrupt(t *testing.T) {
	projects, _ := fixture(t)
	var out bytes.Buffer
	d := Decide(&out, Options{SessionID: sid, ProjectsDir: projects}, true, true,
		func() (string, error) { return "", errors.New("interrupted") })
	if !d.Skip || !strings.Contains(out.String(), "skipped") || !strings.Contains(out.String(), "\033[") {
		t.Fatalf("%+v %q", d, out.String())
	}
}

func TestDecideNowAndNonTTYDoNotWait(t *testing.T) {
	projects, _ := fixture(t)
	called := false
	rl := func() (string, error) { called = true; return "", nil }
	if d := Decide(&bytes.Buffer{}, Options{SessionID: sid, ProjectsDir: projects, Now: true}, true, true, rl); d.Skip || called {
		t.Fatalf("--now must not wait: %+v called=%v", d, called)
	}
	if d := Decide(&bytes.Buffer{}, Options{SessionID: sid, ProjectsDir: projects}, false, false, rl); d.Skip || called {
		t.Fatalf("non-tty stdin must not wait: %+v called=%v", d, called)
	}
}

func TestDecidePrintsSavedOutputAndTeleportLine(t *testing.T) {
	projects, _ := fixture(t)
	saved := filepath.Join(t.TempDir(), "capture.txt")
	os.WriteFile(saved, []byte("old pane content\n"), 0o600)
	var out bytes.Buffer
	Decide(&out, Options{SessionID: sid, ProjectsDir: projects, SavedOutput: saved, Now: true,
		TeleportedTo: "big-storage.example", TeleportedAt: "2026-08-27T12:00:00Z"}, false, true, nil)
	s := out.String()
	if !strings.HasPrefix(s, "old pane content\n") {
		t.Fatalf("saved output must come first:\n%s", s)
	}
	if !strings.Contains(s, "teleported to big-storage.example at 2026-08-27T12:00:00Z") || !strings.Contains(s, "forks") {
		t.Fatalf("teleport line missing:\n%s", s)
	}
}

func TestDecideUnknownSessionStillResumes(t *testing.T) {
	var out bytes.Buffer
	d := Decide(&out, Options{SessionID: "00000000-0000-4000-8000-000000000000", ProjectsDir: t.TempDir(), Now: true}, false, true, nil)
	if d.Skip || d.Chdir != "" || len(d.Argv) != 3 || !strings.Contains(out.String(), "transcript not found") {
		t.Fatalf("%+v %q", d, out.String())
	}
	d = Decide(&out, Options{SessionID: "junk", ProjectsDir: t.TempDir(), Now: true}, false, true, nil)
	if len(d.Argv) != 1 || d.Argv[0] != "claude" {
		t.Fatalf("junk id must open the picker: %+v", d)
	}
}

func TestChdirTarget(t *testing.T) {
	projects, cwd := fixture(t)
	transcript := filepath.Join(projects, session.Munge(cwd), sid+".jsonl")
	if got := ChdirTarget(session.Meta{LaunchCwd: cwd}, transcript); got != cwd {
		t.Fatalf("got %q", got)
	}
	if got := ChdirTarget(session.Meta{LaunchCwd: cwd + "/missing"}, transcript); got != "" {
		t.Fatalf("missing dir: %q", got)
	}
	if got := ChdirTarget(session.Meta{LaunchCwd: filepath.Dir(cwd)}, transcript); got != "" {
		t.Fatalf("munge mismatch: %q", got)
	}
}
