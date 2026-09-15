package tmuxx

import (
	"context"
	"strings"
	"testing"
)

// sessionDial makes each socket a live server answering list-sessions with
// the session names given for it. A socket absent from the map is dead.
func sessionDial(t *testing.T, byPath map[string][]string) (map[string]*Fake, Dialer) {
	t.Helper()
	fakes := map[string]*Fake{}
	for p, names := range byPath {
		lines := make([]string, 0, len(names))
		for _, n := range names {
			lines = append(lines, n+"\t") // name, no group
		}
		fakes[p] = &Fake{Replies: map[string][]string{
			"list-sessions -F \"#{session_name}\t#{session_group}\"": lines,
		}}
	}
	return fakes, func(_ context.Context, p string) (Transport, error) {
		if f, ok := fakes[p]; ok {
			return f, nil
		}
		return nil, ErrNoServer
	}
}

func useDial(t *testing.T, d Dialer) {
	t.Helper()
	restore := Dial
	Dial = d
	t.Cleanup(func() { Dial = restore })
}

// The gap this closes: several live servers, none named like the source's
// socket and none called "default", so discovery gave up and demanded
// --tmux-socket — even though exactly one of them already held the session
// the window was going to open in.
func TestFindServerPrefersTheServerHoldingTheSession(t *testing.T) {
	dir := t.TempDir()
	main := mkSocket(t, dir, "main")
	work := mkSocket(t, dir, "work")
	_, d := sessionDial(t, map[string][]string{
		main: {"default", "bmc"},
		work: {"pcbs"},
	})
	useDial(t, d)

	// Today: "several tmux servers ... (use --tmux-socket NAME)".
	got, err := FindServerForSession(dir, "laptop", "", "pcbs")
	if err != nil {
		t.Fatalf("FindServerForSession: %v", err)
	}
	if got != work {
		t.Errorf("got %q, want the server holding pcbs (%q)", got, work)
	}
}

// The holder outranks the source-socket-name heuristic. Opening a SECOND
// session called pcbs on `main` while the real one sits on `work` is worse
// than ignoring the name hint: the user asked for a session, and exactly
// one exists.
func TestSessionHolderOutranksPreferredName(t *testing.T) {
	dir := t.TempDir()
	main := mkSocket(t, dir, "main")
	work := mkSocket(t, dir, "work")
	_, d := sessionDial(t, map[string][]string{
		main: {"default"},
		work: {"pcbs"},
	})
	useDial(t, d)

	got, err := FindServerForSession(dir, "main", "", "pcbs")
	if err != nil {
		t.Fatalf("FindServerForSession: %v", err)
	}
	if got != work {
		t.Errorf("got %q, want %q: the holder outranks preferredName", got, work)
	}
}

// Two servers holding the same session name is no basis for choosing, so
// discovery falls back to the established order rather than erroring —
// this must not turn a case that works today into a failure.
func TestAmbiguousSessionFallsBackToNameOrder(t *testing.T) {
	dir := t.TempDir()
	main := mkSocket(t, dir, "main")
	work := mkSocket(t, dir, "work")
	_, d := sessionDial(t, map[string][]string{
		main: {"pcbs"},
		work: {"pcbs"},
	})
	useDial(t, d)

	got, err := FindServerForSession(dir, "main", "", "pcbs")
	if err != nil {
		t.Fatalf("FindServerForSession: %v", err)
	}
	if got != main {
		t.Errorf("got %q, want %q from the name order", got, main)
	}
}

// An explicit --tmux-socket is the user naming the server outright; no
// amount of session-holding may override it.
func TestOverrideWinsOverSessionHolder(t *testing.T) {
	dir := t.TempDir()
	main := mkSocket(t, dir, "main")
	work := mkSocket(t, dir, "work")
	_, d := sessionDial(t, map[string][]string{
		main: {"default"},
		work: {"pcbs"},
	})
	useDial(t, d)

	got, err := FindServerForSession(dir, "", "main", "pcbs")
	if err != nil {
		t.Fatalf("FindServerForSession: %v", err)
	}
	if got != main {
		t.Errorf("got %q, want the overridden %q", got, main)
	}
}

// Nobody holds the target: the existing precedence is untouched.
func TestNoHolderKeepsTheExistingOrder(t *testing.T) {
	dir := t.TempDir()
	main := mkSocket(t, dir, "main")
	work := mkSocket(t, dir, "work")
	_, d := sessionDial(t, map[string][]string{
		main: {"default"},
		work: {"bmc"},
	})
	useDial(t, d)

	got, err := FindServerForSession(dir, "main", "", "nowhere")
	if err != nil {
		t.Fatalf("FindServerForSession: %v", err)
	}
	if got != main {
		t.Errorf("got %q, want %q", got, main)
	}
	// ... and with no name match either, the old error still stands and
	// still tells the user how to resolve it.
	_, err = FindServerForSession(dir, "laptop", "", "nowhere")
	if err == nil || !strings.Contains(err.Error(), "--tmux-socket") {
		t.Errorf("err = %v, want the several-servers error naming --tmux-socket", err)
	}
}

// One live server is the overwhelmingly common case and must not pay for
// this: no session scan, so no extra round trip.
func TestSingleLiveServerIsNotScanned(t *testing.T) {
	dir := t.TempDir()
	main := mkSocket(t, dir, "main")
	mkSocket(t, dir, "stale") // a socket file with no server behind it
	fakes, d := sessionDial(t, map[string][]string{main: {"default"}})
	useDial(t, d)

	got, err := FindServerForSession(dir, "laptop", "", "pcbs")
	if err != nil {
		t.Fatalf("FindServerForSession: %v", err)
	}
	if got != main {
		t.Errorf("got %q, want %q", got, main)
	}
	for _, c := range fakes[main].Calls {
		if strings.HasPrefix(c, "list-sessions") {
			t.Errorf("scanned sessions with only one live server: %q", c)
		}
	}
}

// FindServer keeps its meaning: it is FindServerForSession with no target.
func TestFindServerIsTheNoTargetCase(t *testing.T) {
	dir := t.TempDir()
	main := mkSocket(t, dir, "main")
	work := mkSocket(t, dir, "work")
	_, d := sessionDial(t, map[string][]string{
		main: {"default"},
		work: {"pcbs"},
	})
	useDial(t, d)

	if got, err := FindServer(dir, "main", ""); got != main || err != nil {
		t.Errorf("FindServer = %q %v, want %q", got, err, main)
	}
}
