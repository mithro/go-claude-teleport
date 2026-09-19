package orchestrate

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
)

// not-a-repo is the only mode whose cost is unbounded, and it was the only
// one rendering no caveats at all: renderGit printed the two paths and
// returned, and renderGitCaveats returned early for it.
//
// The other modes are bounded by what git tracks. This one walks the whole
// directory -- gitx.Files answers ModeNotRepo with a bare walk() that has
// no .git skip and no gitignore matcher, because with no repository there
// is nothing to read ignore rules from -- so --exclude is the only filter
// there is.
//
// Real case (2026-09-18): eight ten64 sessions have project cwd
// /home/tim/github/fpgas-online, a container of sixteen sibling
// repositories, 429,049 files and 8.1 GB including every nested .git. The
// plan said only "copied as plain files" and gave the reader nothing to
// act on.
func notRepoPlan() *Plan {
	cwd := "/home/tim/github/fpgas-online"
	return &Plan{
		Session: &session.Session{
			ID:         session.ID("7e300f9b-6cdc-49b5-aaa1-b9965450b37e"),
			State:      session.StateSuspended,
			ProjectDir: "/home/tim/.claude/projects/" + session.Munge(cwd),
			LaunchCwd:  cwd,
			WorkCwd:    cwd,
		},
		Git: &gitx.Plan{
			Mode:        gitx.ModeNotRepo,
			SrcWorktree: cwd,
			DstWorktree: cwd,
		},
		SourceInfo: remote.HostInfo{Hostname: "ten64"},
		DestInfo:   remote.HostInfo{Hostname: "desktop"},
		Options:    Options{Direction: "from"},
	}
}

func TestRenderNotRepoWarnsTheWholeTreeTravels(t *testing.T) {
	var b bytes.Buffer
	notRepoPlan().Render(&b)
	out := b.String()

	if !strings.Contains(out, "Caveats") {
		t.Fatalf("not-a-repo plan renders no caveats at all:\n%s", out)
	}
	// The three things a reader has to know to judge this plan.
	for _, want := range []string{
		"every file", // the scope of the copy
		".git",       // nested repositories are not skipped
		"--exclude",  // the only filter available
	} {
		if !strings.Contains(out, want) {
			t.Errorf("not-a-repo caveats never mention %q:\n%s", want, out)
		}
	}
}

// gitignore is specifically worth naming: every other mode honours it, so
// a reader who knows the tool will assume this one does too.
func TestRenderNotRepoSaysGitignoreIsNotConsulted(t *testing.T) {
	var b bytes.Buffer
	notRepoPlan().Render(&b)
	out := b.String()
	if !strings.Contains(out, "gitignore") {
		t.Errorf("not-a-repo caveats do not say gitignore is not consulted:\n%s", out)
	}
}

// Symlinks are the refusal that actually fires for this mode, and it
// fires FIRST: dst.ManifestDiff validates every entry before
// transfer.Blocking ever compares content (preflight.go). A tracked tree
// rarely carries an absolute symlink; an untracked one is full of them,
// because every virtualenv has .venv/bin/python pointing at an
// interpreter outside the tree.
//
// Measured on the real case (a8c34ffa, 8.1 GB, 404k entries walked): 571
// entries refused, 559 of them "symlink target ... is absolute" and 12
// "resolves to ..., outside root". The content-collision check was never
// reached.
func TestRenderNotRepoWarnsAboutSymlinks(t *testing.T) {
	var b bytes.Buffer
	notRepoPlan().Render(&b)
	out := b.String()
	if !strings.Contains(out, "symlink") {
		t.Errorf("not-a-repo caveats do not mention symlinks, which are what\n"+
			"actually refuses this mode in practice:\n%s", out)
	}
}

// The repo modes must keep the caveats they already had -- this guards the
// early-return being replaced rather than deleted.
func TestRenderRepoModesKeepTheirCaveats(t *testing.T) {
	p := notRepoPlan()
	p.Git = &gitx.Plan{
		Mode:        gitx.ModeFreshMain,
		SrcWorktree: "/home/tim/github/mithro/netv2",
		DstWorktree: "/home/tim/github/mithro/netv2",
		DstMain:     "/home/tim/github/mithro/netv2",
	}
	var b bytes.Buffer
	p.Render(&b)
	out := b.String()
	if !strings.Contains(out, "submodule gitlinks transfer stale") {
		t.Errorf("fresh-main lost its existing caveats:\n%s", out)
	}
	if strings.Contains(out, "--exclude is the only filter") {
		t.Errorf("fresh-main should not carry the not-a-repo caveat:\n%s", out)
	}
}
