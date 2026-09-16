package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// seedMovedTranscript writes a transcript recording TWO cwds -- a session
// that changed directory -- filed, as Claude Code files it, under the
// project directory of the work cwd.
func seedMovedTranscript(t *testing.T, p session.Paths, launch, work string) {
	t.Helper()
	proj := p.ProjectDir(work)
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"cwd":"` + launch + `"}` + "\n" + `{"cwd":"` + work + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The destination relaunches Claude in the session's PROJECT cwd (see
// session.ProjectCwd), so that is the directory whose trust dialog the
// destination must already have accepted and whose project entry carries
// the session's per-project state. Reading the LAUNCH cwd instead grants
// trust at a directory the resumed Claude never opens, and it sits at the
// first-run dialog until the teleport gives up confirming it (exit 5).
//
// Real case (2026-09-17): session 1d7814e6 launched in
// wafer-space/tinytapeout-boards and now works in
// fpgas-online/fpgas.online-mechanical. The plan correctly rooted the
// transfer at the latter while the extras carried the former's trust.
func TestSessionExtrasTrustsTheProjectCwdNotTheLaunchCwd(t *testing.T) {
	p := testPaths(t)
	launch := filepath.Join(p.Home, "launched-here")
	work := filepath.Join(p.Home, "works-here")
	seedMovedTranscript(t, p, launch, work)
	// Both directories are known to Claude Code, but only the one the
	// session actually works in is trusted -- picking the wrong entry is
	// therefore visible rather than accidentally right.
	os.WriteFile(p.GlobalJSON, []byte(`{"projects":{`+
		`"`+launch+`":{"hasTrustDialogAccepted":false},`+
		`"`+work+`":{"hasTrustDialogAccepted":true}}}`), 0o600)

	l := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc", Logf: t.Logf})
	pm := session.NewPathMap(session.Mapping{From: p.Home, To: "/home/bob"})
	ex, err := l.SessionExtras(context.Background(), session.ID(sid), pm)
	if err != nil {
		t.Fatal(err)
	}
	if !ex.SourceTrusted {
		t.Error("SourceTrusted = false: the work cwd's accepted trust dialog was not found")
	}
	if want := "/home/bob/works-here"; ex.TrustCwd != want {
		t.Errorf("TrustCwd = %q, want the mapped work cwd %q", ex.TrustCwd, want)
	}
	if !session.TrustAccepted(ex.ProjectEntry) {
		t.Errorf("ProjectEntry = %v, want the work cwd's trusted entry", ex.ProjectEntry)
	}
}

// A session that never left its launch cwd is unaffected: the two cwds
// agree, so the same entry is read either way.
func TestSessionExtrasUnchangedWhenTheCwdsAgree(t *testing.T) {
	p := testPaths(t)
	cwd := filepath.Join(p.Home, "proj")
	seedMovedTranscript(t, p, cwd, cwd)
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
}
