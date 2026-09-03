package remote

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
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

// trustPane is a fake tmux transport whose pane shows Claude Code's
// first-run trust dialog until it receives Down THEN Enter — the two keys
// that move the selection from "❯ No, exit" to "Yes, I trust this folder"
// and answer it. onAccept fires when the dialog is answered, so a test can
// make the registry entry appear exactly then (as a real Claude does).
type trustPane struct {
	mu       sync.Mutex
	cmds     []string
	sawDown  bool
	accepted bool
	// stuck reproduces a dialog whose selection does NOT move when Down
	// arrives (a repainted screen, a keystroke that never landed): Enter
	// would then answer "No, exit" and kill the destination Claude.
	stuck    bool
	onAccept func()
}

// trustDialog renders Claude Code 2.1.259's dialog with the selection
// marker on one of the two choices — the exact spelling captured from the
// real thing in the layer-2 container ("❯ " then the choice; the other
// line indented by the same width).
func trustDialog(yesSelected bool) []string {
	no, yes := "   No, exit", " ❯ Yes, I trust this folder"
	if !yesSelected {
		no, yes = " ❯ No, exit", "   Yes, I trust this folder"
	}
	return []string{
		"Quick safety check: Is this a project you created or one you trust?",
		"take a moment to review what's in this folder first.",
		no, yes,
		"Enter to confirm · Esc to cancel",
	}
}

func (p *trustPane) Run(_ context.Context, cmd string) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cmds = append(p.cmds, cmd)
	switch {
	case strings.HasPrefix(cmd, "capture-pane"):
		if p.accepted {
			return []string{"╭─ Welcome to Claude Code ─╮", "> "}, nil
		}
		return trustDialog(p.sawDown && !p.stuck), nil
	case strings.HasPrefix(cmd, "send-keys"):
		switch {
		case strings.HasSuffix(cmd, " Down"):
			p.sawDown = true
		case strings.HasSuffix(cmd, " Enter") && p.sawDown:
			p.accepted = true
			if p.onAccept != nil {
				p.onAccept()
			}
		}
		return nil, nil
	}
	return nil, fmt.Errorf("trustPane: unexpected command %q", cmd)
}

func (p *trustPane) Close() error { return nil }

func (p *trustPane) sent() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, c := range p.cmds {
		if strings.HasPrefix(c, "send-keys") {
			out = append(out, c)
		}
	}
	return out
}

// TestConfirmClaudeAutoAcceptsTheTrustPromptWhenTheSourceWasTrusted is
// ruling R-P3-TRUST-1 item 2: the destination Claude resumed and is
// waiting at the trust dialog (so there is no registry entry at all). The
// source's own trust dialog was accepted, so confirmation answers this one
// — Down then Enter, into the job's pane and nothing else — and keeps
// waiting for the registry rather than failing.
func TestConfirmClaudeAutoAcceptsTheTrustPromptWhenTheSourceWasTrusted(t *testing.T) {
	p := testPaths(t)
	proc := fakeProcRoot(t, [][4]string{{"5150", "1", "claude", "claude\x00"}})
	pane := &trustPane{}
	pane.onAccept = func() { writeRegistry(t, p, 5150, "idle", "work:@1.%7") }
	l := NewLocal(p, "x", LocalOptions{ProcRoot: proc, Tmux: func(context.Context, string) (tmuxx.Transport, error) { return pane, nil }, Sleep: func(time.Duration) {}, Logf: t.Logf})
	ref := &session.TmuxRef{SocketPath: "/s", Session: "work", WindowID: "@1", PaneID: "%7"}
	reg, err := l.ConfirmClaude(context.Background(), ref, session.ID(sid), 5*time.Second, true)
	if err != nil {
		t.Fatalf("ConfirmClaude = %v, want the trust prompt answered and the session confirmed", err)
	}
	if reg.Status != "idle" {
		t.Errorf("reg = %+v", reg)
	}
	want := []string{`send-keys -t "%7" Down`, `send-keys -t "%7" Enter`}
	if got := pane.sent(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("keys sent = %v, want exactly %v (into the job's pane only, once)", got, want)
	}
}

// TestConfirmClaudeFailsWithTrustAdviceWhenTheSourceWasNotTrusted is the
// other half: with no trust to carry, nothing may be typed into the pane
// on the user's behalf, and the error must say what is actually wrong —
// never the "/login" advice, which sent the first real teleport's user
// looking for a login problem that did not exist.
func TestConfirmClaudeFailsWithTrustAdviceWhenTheSourceWasNotTrusted(t *testing.T) {
	p := testPaths(t)
	proc := fakeProcRoot(t, [][4]string{{"5150", "1", "claude", "claude\x00"}})
	pane := &trustPane{}
	l := NewLocal(p, "x", LocalOptions{ProcRoot: proc, Tmux: func(context.Context, string) (tmuxx.Transport, error) { return pane, nil }, Sleep: func(time.Duration) {}, Logf: t.Logf})
	ref := &session.TmuxRef{SocketPath: "/s", Session: "work", WindowID: "@1", PaneID: "%7"}
	_, err := l.ConfirmClaude(context.Background(), ref, session.ID(sid), 300*time.Millisecond, false)
	if err == nil {
		t.Fatal("ConfirmClaude succeeded with the destination stuck at the trust prompt")
	}
	for _, want := range []string{TrustPromptWaiting, l.Hostname, "work:@1.%7", "claude-teleport continue " + sid} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "/login") {
		t.Errorf("error must not blame login: %q", err)
	}
	if got := pane.sent(); len(got) != 0 {
		t.Errorf("nothing may be typed into the pane without the source's trust; sent %v", got)
	}
}

// TestConfirmClaudeDoesNotPressEnterWhenTheSelectionDidNotMove is PR #11
// review minor 4: the answer is self-validating. Down is only half of it —
// if the pane still shows "❯ No, exit" afterwards, pressing Enter would
// answer "No, exit" and kill the destination Claude, so it is not pressed
// and the failure says which half did not take.
func TestConfirmClaudeDoesNotPressEnterWhenTheSelectionDidNotMove(t *testing.T) {
	p := testPaths(t)
	proc := fakeProcRoot(t, [][4]string{{"5150", "1", "claude", "claude\x00"}})
	pane := &trustPane{stuck: true}
	l := NewLocal(p, "x", LocalOptions{ProcRoot: proc, Tmux: func(context.Context, string) (tmuxx.Transport, error) { return pane, nil }, Sleep: func(time.Duration) {}, Logf: t.Logf})
	ref := &session.TmuxRef{SocketPath: "/s", Session: "work", WindowID: "@1", PaneID: "%7"}
	_, err := l.ConfirmClaude(context.Background(), ref, session.ID(sid), 300*time.Millisecond, true)
	if err == nil {
		t.Fatal("ConfirmClaude succeeded with the destination stuck at the trust prompt")
	}
	if !strings.Contains(err.Error(), "did not move") {
		t.Errorf("error %q should say the selection never moved", err)
	}
	for _, c := range pane.sent() {
		if strings.HasSuffix(c, " Enter") {
			t.Errorf("Enter must not be pressed while \"No, exit\" is selected; sent %v", pane.sent())
		}
	}
}
