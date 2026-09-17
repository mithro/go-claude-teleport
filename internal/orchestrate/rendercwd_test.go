package orchestrate

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
)

// The "cwd" line is what a human reads to check the plan is about the
// directory they meant. For a session that changed directory it printed
// the LAUNCH cwd while every decision under it -- the repository
// transferred, the destination cwd, the directory the window opens in --
// was made from the PROJECT cwd, so the summary named a directory the
// teleport does not touch.
//
// Real case (2026-09-17): session 1d7814e6 printed
//
//	cwd    /home/tim/github/wafer-space/tinytapeout-boards
//
// above a git plan for /home/tim/github/fpgas-online/fpgas.online-mechanical.
func TestRenderShowsTheProjectCwd(t *testing.T) {
	launch := "/home/tim/github/wafer-space/tinytapeout-boards"
	work := "/home/tim/github/fpgas-online/fpgas.online-mechanical"
	p := &Plan{
		Session: &session.Session{
			ID:         session.ID("1d7814e6-e32a-4cbc-94e9-5dd20697d603"),
			State:      session.StateRunning,
			ProjectDir: "/home/tim/.claude/projects/" + session.Munge(work),
			LaunchCwd:  launch,
			WorkCwd:    work,
		},
		SourceInfo: remote.HostInfo{Hostname: "x1c-work"},
		DestInfo:   remote.HostInfo{Hostname: "desktop"},
		Options:    Options{Direction: "from"},
	}
	var b bytes.Buffer
	p.Render(&b)
	out := b.String()

	if !strings.Contains(out, "cwd    "+work) {
		t.Errorf("summary does not name the project cwd %q:\n%s", work, out)
	}
	if strings.Contains(out, "cwd    "+launch) {
		t.Errorf("summary names the launch cwd %q, which is not what is moved:\n%s", launch, out)
	}
}
