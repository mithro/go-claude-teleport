// internal/orchestrate/render_test.go
package orchestrate

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/claudecfg"
	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

func TestRenderMentionsEveryDecision(t *testing.T) {
	p := &Plan{
		Options:     Options{Direction: "to", Target: "bob@big-storage.example", Via: []string{"jump.example"}},
		Session:     &session.Session{ID: session.ID(sid), LaunchCwd: "/home/alice/github/x/.worktrees/feat", Branch: "feat", State: session.StateRunning},
		SourceInfo:  remote.HostInfo{Hostname: "laptop.example", Home: "/home/alice", ClaudeVersion: "2.1.247"},
		DestInfo:    remote.HostInfo{Hostname: "big-storage.example", Home: "/home/bob", ClaudeVersion: "2.1.250"},
		PathMap:     session.NewPathMap(session.Mapping{From: "/home/alice", To: "/home/bob"}),
		Git:         &gitx.Plan{Mode: gitx.ModeExistingMain, DstMain: "/home/bob/github/x", DstWorktree: "/home/bob/github/x/.worktrees/feat", Branch: "feat", Tip: "cccccccccccccccccccccccccccccccccccccccc", NeedPack: true, FastForward: true, Dirty: gitx.Dirty{Modified: []string{"a.go"}, Untracked: []string{"b.go"}}},
		Tmux:        &tmuxx.Plan{SocketPath: "/tmp/tmux-1001/default", Group: "work", WindowName: "claude", CreateSession: true},
		TargetState: "running",
		Drift:       claudecfg.Report{Diffs: []claudecfg.Difference{{Class: claudecfg.Warn, Key: "claude.version", Source: "2.1.247", Dest: "2.1.250"}}},
		Statuses:    map[int]transfer.Status{1: transfer.Absent, 2: transfer.PresentSame, 3: transfer.FFCandidate},
	}
	var buf bytes.Buffer
	p.Render(&buf)
	out := buf.String()
	for _, want := range []string{"3f2a9c1e", "laptop.example", "big-storage.example", "via jump.example", "/home/alice -> /home/bob", "existing-main", "fast-forward", "packfile", "a.go", "b.go", "new session \"work\"", "window \"claude\"", "running", "claude.version", "2 to send", "1 already present", "1 fast-forward"} {
		if !strings.Contains(out, want) {
			t.Errorf("render lacks %q:\n%s", want, out)
		}
	}
}

// basePlan returns a minimal renderable Plan for the caveat-condition tests
// below (sanitised identities; not-repo git plan by default).
func basePlan() *Plan {
	return &Plan{
		Options:     Options{Direction: "to"},
		Session:     &session.Session{ID: session.ID(sid), LaunchCwd: "/home/alice/x", State: session.StateIdle},
		SourceInfo:  remote.HostInfo{Hostname: "laptop.example"},
		DestInfo:    remote.HostInfo{Hostname: "dest.private"},
		Git:         &gitx.Plan{Mode: gitx.ModeNotRepo, SrcWorktree: "/home/alice/x", DstWorktree: "/home/bob/x"},
		TargetState: "idle",
	}
}

// TestRenderCaveatsNotRepo: no git repo at all -> no git caveats section,
// since the caveats concern git-specific limitations.
func TestRenderCaveatsNotRepo(t *testing.T) {
	p := basePlan()
	var buf bytes.Buffer
	p.Render(&buf)
	out := buf.String()
	for _, unwanted := range []string{"submodule", "relativeworktrees", "staged deletions", "include-ignored"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("not-repo render should not mention %q:\n%s", unwanted, out)
		}
	}
}

// TestRenderCaveatsRepoPlanAlways: the two unconditional repo-plan notes
// (submodule gitlinks stale, relativeworktrees unsupported) appear for
// EVERY repo plan (fresh-main and existing-main), regardless of other
// state — gitx.Plan carries no Submodules field to gate on, so the
// controller ruling says render unconditionally for repo plans.
func TestRenderCaveatsRepoPlanAlways(t *testing.T) {
	for _, mode := range []gitx.Mode{gitx.ModeFreshMain, gitx.ModeExistingMain} {
		p := basePlan()
		p.Git = &gitx.Plan{Mode: mode, DstMain: "/home/bob/x", DstWorktree: "/home/bob/x"}
		var buf bytes.Buffer
		p.Render(&buf)
		out := buf.String()
		for _, want := range []string{"submodule", "relativeworktrees", ".git/info/exclude"} {
			if !strings.Contains(out, want) {
				t.Errorf("mode %s: render lacks %q:\n%s", mode, want, out)
			}
		}
	}
}

// TestRenderCaveatsStagedDeletionsGated: the "staged deletions do not
// travel" note appears only for existing-main WITH dirty/staged state
// carried, not for a clean existing-main plan and not for fresh-main.
func TestRenderCaveatsStagedDeletionsGated(t *testing.T) {
	cases := []struct {
		name string
		git  *gitx.Plan
		want bool
	}{
		{"existing-main dirty", &gitx.Plan{Mode: gitx.ModeExistingMain, DstMain: "/home/bob/x", DstWorktree: "/home/bob/x", Dirty: gitx.Dirty{Staged: []string{"a.go"}}}, true},
		{"existing-main clean", &gitx.Plan{Mode: gitx.ModeExistingMain, DstMain: "/home/bob/x", DstWorktree: "/home/bob/x"}, false},
		{"fresh-main dirty", &gitx.Plan{Mode: gitx.ModeFreshMain, DstMain: "/home/bob/x", DstWorktree: "/home/bob/x", Dirty: gitx.Dirty{Staged: []string{"a.go"}}}, false},
	}
	for _, c := range cases {
		p := basePlan()
		p.Git = c.git
		var buf bytes.Buffer
		p.Render(&buf)
		got := strings.Contains(buf.String(), "staged deletions")
		if got != c.want {
			t.Errorf("%s: staged-deletions note present=%v, want %v:\n%s", c.name, got, c.want, buf.String())
		}
	}
}

// TestRenderCaveatsIncludeIgnoredGated: the "--include-ignored is inert"
// note appears only when the flag is set AND the git plan is existing-main.
func TestRenderCaveatsIncludeIgnoredGated(t *testing.T) {
	cases := []struct {
		name           string
		includeIgnored bool
		mode           gitx.Mode
		want           bool
	}{
		{"existing-main, flag set", true, gitx.ModeExistingMain, true},
		{"existing-main, flag unset", false, gitx.ModeExistingMain, false},
		{"fresh-main, flag set", true, gitx.ModeFreshMain, false},
	}
	for _, c := range cases {
		p := basePlan()
		p.Options.IncludeIgnored = c.includeIgnored
		p.Git = &gitx.Plan{Mode: c.mode, DstMain: "/home/bob/x", DstWorktree: "/home/bob/x"}
		var buf bytes.Buffer
		p.Render(&buf)
		got := strings.Contains(buf.String(), "include-ignored")
		if got != c.want {
			t.Errorf("%s: include-ignored note present=%v, want %v:\n%s", c.name, got, c.want, buf.String())
		}
	}
}
