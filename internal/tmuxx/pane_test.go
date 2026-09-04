package tmuxx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/session"
)

// listPanesCmd is the exact command prober.ListPanes issues (tab-separated,
// see listPanesFormat).
const listPanesCmd = "list-panes -a -F \"#{session_name}\t#{window_id}\t#{pane_id}\""

// TestCaptureScreenReadsOnlyTheVisiblePane is ruling R-P3-TRUST-1 item 2:
// "is Claude showing its trust dialog right now" is a question about the
// pane's CURRENT screen — the scrollback may still hold the same dialog
// from an earlier, already-answered attempt, and answering that one again
// would type Down+Enter at a live Claude. So the screen capture must not
// carry -S - (the whole history) the way Capture does.
func TestCaptureScreenReadsOnlyTheVisiblePane(t *testing.T) {
	f := &Fake{Replies: map[string][]string{`capture-pane -epJ -t "%7"`: {"on screen", "> "}}}
	got, err := CaptureScreen(context.Background(), f, "%7")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "on screen\n> \n" {
		t.Errorf("capture = %q", got)
	}
	if _, err := CaptureScreen(context.Background(), f, "7"); err == nil {
		t.Error("a pane id without its sigil must be refused")
	}
}

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
	//
	// The text goes in as literal bytes and the newline as the literal CR
	// byte (ruling R-P3-PROOF-5 item 2): a pane whose stopped Claude left
	// the terminal in extended-keys mode makes tmux encode key NAMES in a
	// form the shell that inherited it cannot read.
	want := []string{
		`send-keys -l -t "%7" " 'claude-teleport' 'placeholder' '--resume' '3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c' '--saved-output' '/home/bob/it'\\''s.txt' '--now'"`,
		`send-keys -H -t "%7" 0d`,
	}
	f := &Fake{Replies: map[string][]string{want[0]: {}, want[1]: {}}}
	err := TypeCommand(context.Background(), f, "%7", []string{"claude-teleport", "placeholder", "--resume", "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c", "--saved-output", "/home/bob/it's.txt", "--now"})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, f.Calls); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

// TestSendReturnIsTheLiteralCRByte pins the newline half of R-P3-PROOF-5
// item 2: `send-keys -H 0d`, the byte a terminal's Enter key produces,
// never the "Enter" key NAME (which tmux re-encodes for an application
// that turned extended keys on) and never C-u/C-c to clear a line.
func TestSendReturnIsTheLiteralCRByte(t *testing.T) {
	f := &Fake{Replies: map[string][]string{`send-keys -H -t "%7" 0d`: {}}}
	if err := SendReturn(context.Background(), f, "%7"); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{`send-keys -H -t "%7" 0d`}, f.Calls); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

// TestSendLiteralDoesNotInterpretKeyNames: text that happens to spell a
// tmux key name is still just text under -l.
func TestSendLiteralDoesNotInterpretKeyNames(t *testing.T) {
	f := &Fake{Default: []string{}}
	if err := SendLiteral(context.Background(), f, "%7", "Enter C-u"); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{`send-keys -l -t "%7" "Enter C-u"`}, f.Calls); diff != "" {
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
		`list-panes -t "%7" -F "#{pane_pid}"`:     {"100"},
		`capture-pane -p -S -50 -t "%7"`:          {},
		`list-panes -t "=main:2" -F "#{pane_id}"`: {"%7", "%8"},
		listPanesCmd:    {"main\t@1\t%7", "main\t@1\t%8"},
		listSessionsCmd: {"main\tmain"},
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

// TestFindWindowMatchesDecodedSpelling covers R-PRB-9: a human types the
// plain, decoded session name at the CLI (`claude-teleport <tmux-session>
// <window> --to ...`), but tmux STORES (and only ever targets) the vis(3)-
// encoded spelling. FindWindow must resolve the typed name against both
// spellings of every session tmux reports.
//
// stored `p\\q` (doubled backslash) decodes, via UnvisName, to `p\q` (one
// backslash) — 'q' is not a recognised vis escape letter (unlike 'b', 't',
// 'n', ... it is preserved literally), so `p\q` on its own decodes to
// itself; only the doubled form collapses to it.
func TestFindWindowMatchesDecodedSpelling(t *testing.T) {
	tb := fakeProc(t, nil)
	f := &Fake{Replies: map[string][]string{
		listSessionsCmd: {`p\\q` + "\tmain", "other\tmain"},
		`list-panes -t "=p\\\\q:2" -F "#{pane_id}"`: {"%7"},
	}}
	p := Prober(context.Background(), f, tb, "/tmp/tmux-1000/default")
	// The human types the plain, decoded name `p\q` (one backslash); the
	// stored spelling is `p\\q` (doubled).
	panes, err := p.FindWindow(`p\q`, "2")
	if err != nil || len(panes) != 1 || panes[0] != "%7" {
		t.Fatalf("FindWindow(decoded name) = %v %v", panes, err)
	}
}

// TestFindWindowAmbiguousBetweenSpellings covers R-PRB-9's ambiguity rule:
// if the typed name matches two DIFFERENT stored sessions (one verbatim,
// one only after decoding), that is an error, not a silent pick.
func TestFindWindowAmbiguousBetweenSpellings(t *testing.T) {
	tb := fakeProc(t, nil)
	f := &Fake{Replies: map[string][]string{
		// Session "g1" is stored as `p\\q`, which DECODES to `p\q`.
		// Session "g2" is literally stored as `p\q` (matches the typed
		// text verbatim). Both match the typed string `p\q`.
		listSessionsCmd: {`p\\q` + "\tg1", `p\q` + "\tg2"},
	}}
	p := Prober(context.Background(), f, tb, "/tmp/tmux-1000/default")
	_, err := p.FindWindow(`p\q`, "2")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("FindWindow(ambiguous name) err = %v, want an ambiguity error", err)
	}
}

// TestFindWindowAmbiguityNamesStoredSpellings covers B7. The candidates in
// the ambiguity message used to be run back through UnvisName — but
// decoding them is exactly what made the two indistinguishable, so the
// message read "ambiguous between sessions: a b, a b" and told the user
// nothing about which `-t` target would disambiguate. The stored spellings
// are the only useful thing to print.
func TestFindWindowAmbiguityNamesStoredSpellings(t *testing.T) {
	tb := fakeProc(t, nil)
	f := &Fake{Replies: map[string][]string{
		// Two different vis octal encodings of the same text, "a b"
		// (3-digit and 2-digit octal for the space both decode the same
		// way; UnvisName does not require a fixed digit count).
		listSessionsCmd: {`a\040b` + "\tg1", `a\40b` + "\tg2"},
	}}
	p := Prober(context.Background(), f, tb, "/tmp/tmux-1000/default")
	_, err := p.FindWindow("a b", "2")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("FindWindow(ambiguous name) err = %v, want an ambiguity error", err)
	}
	_, list, ok := strings.Cut(err.Error(), "sessions: ")
	if !ok {
		t.Fatalf("err = %v, want it to list the candidate sessions", err)
	}
	if list != `a\040b, a\40b` {
		t.Errorf("candidate list = %q, want the two stored spellings", list)
	}
}

// TestProberListPanesKeepsStoredSessionNames replaces the Task 10 fixture
// that asserted tmux reports a space as `\s` (I2). It does not: a space is
// printable and vis leaves it alone, so the old space-separated format split
// such a name in two and the pane was silently dropped from suspended-pane
// discovery. Transcribed from a live probe on a throwaway socket (tmux
// next-3.8), recorded in the fix-wave report:
//
//	$ tmux new-session -d -s 'a b'; tmux new-session -d -s 'a\b'
//	$ tmux new-session -d -s 'a"b'
//	$ tmux list-panes -a -F '#{session_name}<TAB>#{window_id}<TAB>#{pane_id}'
//	a b<TAB>@0<TAB>%0
//	a"b<TAB>@2<TAB>%2
//	a\\b<TAB>@1<TAB>%1
//
// Names keep their stored spelling — that is what a `-t` target needs.
func TestProberListPanesKeepsStoredSessionNames(t *testing.T) {
	tb := fakeProc(t, nil)
	f := &Fake{Replies: map[string][]string{
		listPanesCmd: {"a b\t@0\t%0", "a\"b\t@2\t%2", `a\\b` + "\t@1\t%1"},
	}}
	p := Prober(context.Background(), f, tb, "/tmp/tmux-1000/default")
	all, err := p.ListPanes()
	if err != nil {
		t.Fatal(err)
	}
	want := []session.PaneInfo{
		{Session: `a b`, WindowID: "@0", PaneID: "%0"},
		{Session: `a"b`, WindowID: "@2", PaneID: "%2"},
		{Session: `a\\b`, WindowID: "@1", PaneID: "%1"},
	}
	if diff := cmp.Diff(want, all); diff != "" {
		t.Errorf("ListPanes (-want +got):\n%s", diff)
	}
}

// TestProberListPanesRejectsMalformedLine: a wrong field count must be an
// error, never a `continue` — a dropped pane is an invisible session, and
// step 7 would then open a second window for a session that already has one.
func TestProberListPanesRejectsMalformedLine(t *testing.T) {
	f := &Fake{Replies: map[string][]string{listPanesCmd: {"nope"}}}
	p := Prober(context.Background(), f, fakeProc(t, nil), "/tmp/tmux-1000/default")
	all, err := p.ListPanes()
	if err == nil {
		t.Fatalf("ListPanes = %+v, want an error on a malformed line", all)
	}
}

// TestTargetIDSigilRequired is the I6 guard: tmux resolves an empty -t
// target to the CURRENT pane/window, so every entry point that takes an id
// rejects one without its sigil — before it says anything to tmux. Ids
// cross the JSON wire into a long-lived `remote serve`, and the safety
// criterion is "never touch panes the tool did not name".
func TestTargetIDSigilRequired(t *testing.T) {
	ctx := context.Background()
	tb := fakeProc(t, nil)
	for _, bad := range []string{"", "7", "@1", "pane%1"} {
		for _, tc := range []struct {
			op  string
			run func(f *Fake) error
		}{
			{"Capture", func(f *Fake) error { _, err := Capture(ctx, f, bad); return err }},
			{"SendKeys", func(f *Fake) error { return SendKeys(ctx, f, bad, "Enter") }},
			{"SendLiteral", func(f *Fake) error { return SendLiteral(ctx, f, bad, "fg") }},
			{"SendReturn", func(f *Fake) error { return SendReturn(ctx, f, bad) }},
			{"TypeCommand", func(f *Fake) error { return TypeCommand(ctx, f, bad, []string{"echo", "hi"}) }},
			{"State", func(f *Fake) error { _, err := State(ctx, f, bad, tb); return err }},
		} {
			f := &Fake{Default: []string{}} // would answer ANY command
			if err := tc.run(f); err == nil {
				t.Errorf("%s(%q) = nil, want an error", tc.op, bad)
			} else if !strings.Contains(err.Error(), bad) && bad != "" {
				t.Errorf("%s(%q) error %q does not name the offending id", tc.op, bad, err)
			}
			if len(f.Calls) != 0 {
				t.Errorf("%s(%q) talked to tmux anyway: %v", tc.op, bad, f.Calls)
			}
		}
	}
	// KillWindow takes a window id: "@" is required, a pane id is not one.
	for _, bad := range []string{"", "1", "%7", "win@1"} {
		f := &Fake{Default: []string{}}
		if err := KillWindow(ctx, f, bad); err == nil {
			t.Errorf("KillWindow(%q) = nil, want an error", bad)
		}
		if len(f.Calls) != 0 {
			t.Errorf("KillWindow(%q) talked to tmux anyway: %v", bad, f.Calls)
		}
	}
}
