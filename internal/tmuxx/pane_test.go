package tmuxx

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mithro/go-claude-teleport/internal/procx"
)

func TestCaptureJoinsLinesWithNewline(t *testing.T) {
	f := &Fake{Replies: map[string][]string{`capture-pane -epJ -S - -t "%7"`: {"line1", "", "line3"}}}
	got, err := Capture(context.Background(), f, "%7")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "line1\n\nline3\n" {
		t.Errorf("capture = %q", got)
	}
}

func TestSendKeysQuotesEachKey(t *testing.T) {
	f := &Fake{Replies: map[string][]string{`send-keys -t "%7" "/exit"`: {}, `send-keys -t "%7" Enter`: {}}}
	if err := SendKeys(context.Background(), f, "%7", "/exit"); err != nil {
		t.Fatal(err)
	}
	if err := SendKeys(context.Background(), f, "%7", "Enter"); err != nil {
		t.Fatal(err)
	}
}

func TestTypeCommandLeadingSpaceAndShellQuoting(t *testing.T) {
	// NOTE: the brief's "want" string had a single backslash before ''s.txt';
	// Quote() (see quote.go) unconditionally doubles every literal backslash
	// byte in its input so tmux's own double-quote parser reduces it back to
	// one backslash when it reaches the pane — verified directly against
	// ShellQuote+Quote's actual output. Two backslashes here is correct;
	// see task-11-report.md for the trace.
	want := `send-keys -t "%7" " 'claude-teleport' 'placeholder' '--resume' '3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c' '--saved-output' '/home/bob/it'\\''s.txt' '--now'" Enter`
	f := &Fake{Replies: map[string][]string{want: {}}}
	err := TypeCommand(context.Background(), f, "%7", []string{"claude-teleport", "placeholder", "--resume", "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c", "--saved-output", "/home/bob/it's.txt", "--now"})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{want}, f.Calls); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

// fakeProc writes a /proc-shaped tree for procx.Scan (pid, ppid, comm, cmdline).
func fakeProc(t *testing.T, procs [][4]string) *procx.Table {
	t.Helper()
	root := t.TempDir()
	for _, p := range procs {
		dir := filepath.Join(root, p[0])
		os.MkdirAll(dir, 0o755)
		stat := p[0] + " (" + p[2] + ") S " + p[1] + " 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 12345 0 0"
		os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644)
		os.WriteFile(filepath.Join(dir, "cmdline"), []byte(p[3]), 0o644)
	}
	tb, err := procx.Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func TestStateForegroundChild(t *testing.T) {
	tb := fakeProc(t, [][4]string{
		{"100", "1", "bash", "bash\x00"},
		{"200", "100", "claude-teleport", "claude-teleport\x00placeholder\x00--resume\x003f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c\x00"},
	})
	f := &Fake{Replies: map[string][]string{
		`list-panes -t "%7" -F "#{pane_pid}"`: {"100"},
		`capture-pane -p -S -50 -t "%7"`:      {"a", "b"},
	}}
	st, err := State(context.Background(), f, "%7", tb)
	if err != nil {
		t.Fatal(err)
	}
	want := &PaneState{PaneID: "%7", Command: "claude-teleport", PID: 200,
		Argv: []string{"claude-teleport", "placeholder", "--resume", "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c"}, Content: []string{"a", "b"}}
	if diff := cmp.Diff(want, st); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	if _, ok := procx.IsPlaceholderArgv(st.Argv); !ok {
		t.Error("placeholder argv must keep being recognised by procx.IsPlaceholderArgv")
	}
}

func TestStateBareShell(t *testing.T) {
	tb := fakeProc(t, [][4]string{{"100", "1", "zsh", "-zsh\x00"}})
	f := &Fake{Replies: map[string][]string{`list-panes -t "%7" -F "#{pane_pid}"`: {"100"}, `capture-pane -p -S -50 -t "%7"`: {}}}
	st, err := State(context.Background(), f, "%7", tb)
	if err != nil {
		t.Fatal(err)
	}
	if st.Command != "zsh" || st.PID != 100 {
		t.Errorf("state = %+v", st)
	}
}

func TestProber(t *testing.T) {
	tb := fakeProc(t, [][4]string{{"100", "1", "bash", "bash\x00"}, {"200", "100", "claude", "claude\x00"}})
	f := &Fake{Replies: map[string][]string{
		`list-panes -t "%7" -F "#{pane_pid}"`:                        {"100"},
		`capture-pane -p -S -50 -t "%7"`:                             {},
		`list-panes -t "=main:2" -F "#{pane_id}"`:                    {"%7", "%8"},
		`list-panes -a -F "#{session_name} #{window_id} #{pane_id}"`: {"main @1 %7", "main @1 %8"},
	}}
	p := Prober(context.Background(), f, tb, "/tmp/tmux-1000/default")
	argv, pid, ok := p.PaneCommand("%7")
	if !ok || pid != 200 || len(argv) != 1 || argv[0] != "claude" {
		t.Errorf("PaneCommand = %v %d %v", argv, pid, ok)
	}
	if _, _, ok := p.PaneCommand("%99"); ok {
		t.Error("unknown pane must be ok=false")
	}
	panes, err := p.FindWindow("main", "2")
	if err != nil || len(panes) != 2 {
		t.Errorf("FindWindow = %v %v", panes, err)
	}
	if p.SocketPath() != "/tmp/tmux-1000/default" {
		t.Error("SocketPath")
	}
	all, err := p.ListPanes()
	if err != nil || len(all) != 2 || all[1].Session != "main" || all[1].WindowID != "@1" || all[1].PaneID != "%8" {
		t.Errorf("ListPanes = %v %v", all, err)
	}
}

// CARRIED RULING: ListPanes must UnvisName-decode session names — tmux
// vis(3)-encodes session names at creation time (see UnvisName), so a name
// containing a space comes back over list-panes as e.g. `a\sb`.
func TestProberListPanesDecodesSessionNames(t *testing.T) {
	tb := fakeProc(t, nil)
	f := &Fake{Replies: map[string][]string{
		`list-panes -a -F "#{session_name} #{window_id} #{pane_id}"`: {`a\sb @1 %7`},
	}}
	p := Prober(context.Background(), f, tb, "/tmp/tmux-1000/default")
	all, err := p.ListPanes()
	if err != nil || len(all) != 1 || all[0].Session != "a b" {
		t.Errorf("ListPanes = %+v %v", all, err)
	}
}
