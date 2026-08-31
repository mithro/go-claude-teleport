package remote

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
	"github.com/mithro/go-claude-teleport/internal/version"
)

const sid = "9c8b7a6f-5e4d-4c3b-a2b1-0f9e8d7c6b5a"

func testPaths(t *testing.T) session.Paths {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home", "bob")
	cfg := filepath.Join(home, ".claude")
	os.MkdirAll(cfg, 0o700)
	return session.Paths{Home: home, ConfigDir: cfg, GlobalJSON: filepath.Join(home, ".claude.json"), DataDir: filepath.Join(home, ".local", "share", "claude-teleport")}
}

// sourceManifest builds a one-file manifest whose Dst lands in p.ConfigDir.
// Controller ruling 3: the mapping's From must actually prefix the paths
// that appear both on disk AND inside the JSON content being rewritten
// (session.PathMap.Apply only rewrites a literal prefix match) — so the
// transcript's "cwd" embeds the SAME sandboxed home the mapping's From is
// built from, rather than an unrelated hardcoded "/home/alice" that no
// mapping rooted in a t.TempDir() could ever match. This mirrors
// internal/transfer/manifest_test.go's sourceTree/bobHome pattern (not
// reusable here: it lives in a _test.go file of another package).
func sourceManifest(t *testing.T, p session.Paths) *transfer.Manifest {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home", "alice")
	srcCfg := filepath.Join(home, ".claude")
	rel := "projects/-home-alice-work/" + sid + ".jsonl"
	os.MkdirAll(filepath.Dir(filepath.Join(srcCfg, rel)), 0o700)
	os.WriteFile(filepath.Join(srcCfg, rel), []byte(`{"cwd":"`+home+`/work","sessionId":"`+sid+`"}`+"\n"), 0o600)
	files := []session.FileEntry{{Root: srcCfg, Rel: rel, Category: session.CatSession, Mode: 0o600, ModTime: time.Now(), Rewrite: true}}
	pm := session.NewPathMap(session.Mapping{From: home, To: p.Home})
	m, err := transfer.Build(context.Background(), sid, session.ID(sid), "laptop.example", "big-storage.example", files, pm)
	if err != nil {
		t.Fatal(err)
	}
	m.TmpDir = t.TempDir()
	return m
}

func TestLocalHelloAndPaths(t *testing.T) {
	p := testPaths(t)
	l := NewLocal(p, "/usr/local/bin/claude-teleport", LocalOptions{ProcRoot: "/proc", Logf: t.Logf})
	info, err := l.Hello(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Protocol != version.Protocol || info.Version != version.Version || info.Home != p.Home || info.ConfigDir != p.ConfigDir || info.DataDir != p.DataDir || info.Hostname == "" || info.OS == "" || info.Arch == "" {
		t.Errorf("Hello = %+v", info)
	}
	// Controller ruling 2: LocalOptions.ProcRoot is wired into the
	// session.Paths Local carries (the package-level default was removed).
	want := p
	want.ProcRoot = "/proc"
	if l.Paths() != want {
		t.Errorf("Paths = %+v, want %+v", l.Paths(), want)
	}
}

func TestLocalProcRootDefaultsToProc(t *testing.T) {
	l := NewLocal(testPaths(t), "self", LocalOptions{Logf: t.Logf})
	if got := l.Paths().ProcRoot; got != "/proc" {
		t.Errorf("ProcRoot = %q, want /proc (default)", got)
	}
}

func TestLocalHelloTmuxSocketDirDoesNotReadEnv(t *testing.T) {
	// Controller ruling 1: Local must not read the environment. Set
	// TMUX_TMPDIR to prove Hello ignores it either way.
	t.Setenv("TMUX_TMPDIR", "/should/be/ignored")
	p := testPaths(t)

	l := NewLocal(p, "self", LocalOptions{Logf: t.Logf})
	info, err := l.Hello(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.TmuxSocketDir != "" {
		t.Errorf("TmuxSocketDir = %q, want empty (unknown) when LocalOptions.TmuxSocketDir is unset", info.TmuxSocketDir)
	}

	l2 := NewLocal(p, "self", LocalOptions{TmuxSocketDir: "/run/tmux-1000", Logf: t.Logf})
	info2, err := l2.Hello(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info2.TmuxSocketDir != "/run/tmux-1000" {
		t.Errorf("TmuxSocketDir = %q, want the LocalOptions value verbatim (not derived from TMUX_TMPDIR)", info2.TmuxSocketDir)
	}
}

// writeFakeClaude drops an executable "claude" script into a fresh temp dir
// and returns the dir (to be prepended to PATH). This is not an
// exec.Command LookPath of a name that "may be absent in CI": the test
// controls PATH so the resolved binary is deterministic and always present.
func writeFakeClaude(t *testing.T, exitCode int, output string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho '" + output + "'\nexit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLocalHelloClaudeVersion(t *testing.T) {
	p := testPaths(t)
	oldPath := os.Getenv("PATH")

	t.Run("success", func(t *testing.T) {
		dir := writeFakeClaude(t, 0, "2.1.999 (Claude Code)")
		t.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)
		l := NewLocal(p, "self", LocalOptions{Logf: t.Logf})
		info, err := l.Hello(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !info.HasClaude || info.ClaudeVersion != "2.1.999 (Claude Code)" || info.ClaudeVersionErr != "" {
			t.Errorf("Hello = %+v", info)
		}
	})

	t.Run("failure is not swallowed", func(t *testing.T) {
		// Controller ruling 5: `claude --version`'s error must surface.
		dir := writeFakeClaude(t, 1, "boom")
		t.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)
		l := NewLocal(p, "self", LocalOptions{Logf: t.Logf})
		info, err := l.Hello(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !info.HasClaude {
			t.Fatalf("HasClaude = false, want true (LookPath found the fake binary)")
		}
		if info.ClaudeVersionErr == "" {
			t.Errorf("ClaudeVersionErr empty, want the exec failure reported")
		}
		if info.ClaudeVersion != "" {
			t.Errorf("ClaudeVersion = %q, want empty on failure", info.ClaudeVersion)
		}
	})
}

func TestLocalManifestDiffStreamInstall(t *testing.T) {
	p := testPaths(t)
	l := NewLocal(p, "self", LocalOptions{Logf: t.Logf})
	m := sourceManifest(t, p)
	ctx := context.Background()

	st, err := l.ManifestDiff(ctx, m, sid)
	if err != nil {
		t.Fatal(err)
	}
	if st[0] != transfer.Absent {
		t.Fatalf("status = %v", st)
	}
	if _, err := os.Stat(filepath.Join(job.Dir(p.DataDir, sid), "manifest.json")); err != nil {
		t.Errorf("ManifestDiff must persist the manifest on the destination: %v", err)
	}

	var buf bytes.Buffer
	if err := transfer.Send(ctx, m, transfer.Need(m, st), &buf, nil); err != nil {
		t.Fatal(err)
	}
	s, err := l.OpenStream(ctx, StreamTar, sid, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(s, &buf); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close must return the receive result: %v", err)
	}
	st, _ = l.ManifestDiff(ctx, m, sid)
	if st[0] != transfer.StagedSame {
		t.Fatalf("after stream: %v", st)
	}

	if err := l.PutInstallExtras(ctx, sid, transfer.InstallExtras{ProjectCwd: "/home/bob/work", ProjectEntry: session.ProjectEntry{"hasTrustDialogAccepted": true}}); err != nil {
		t.Fatal(err)
	}
	rep, err := l.Install(ctx, m, sid)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Installed != 1 || !rep.ProjectEntryAdded {
		t.Errorf("report = %+v", rep)
	}
	got, _ := os.ReadFile(m.Entries[0].Dst)
	if !bytes.Contains(got, []byte(`"cwd":"`+p.Home+`/work"`)) {
		t.Errorf("installed transcript not rewritten: %s", got)
	}

	// a corrupt tar stream: Close reports it and nothing is installed twice
	s2, _ := l.OpenStream(ctx, StreamTar, sid, "s2")
	io.WriteString(s2, "definitely not gzip")
	if err := s2.Close(); err == nil {
		t.Errorf("corrupt stream must fail on Close")
	}
}

func TestLocalCaptureAndLogStreams(t *testing.T) {
	p := testPaths(t)
	l := NewLocal(p, "self", LocalOptions{Logf: t.Logf})
	j, _ := job.New(p.DataDir, sid)
	os.WriteFile(j.CapturePath(), []byte("pane contents\n"), 0o600)
	os.WriteFile(j.LogPath(), []byte("log line\n"), 0o600)
	for kind, want := range map[StreamKind]string{StreamCapture: "pane contents\n", StreamLog: "log line\n"} {
		s, err := l.OpenStream(context.Background(), kind, sid, "x")
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(s)
		s.Close()
		if string(data) != want {
			t.Errorf("%s stream = %q", kind, data)
		}
	}
	if _, err := l.OpenStream(context.Background(), StreamPack, sid, "x"); !isUnavailable(err) {
		t.Errorf("pack stream err = %v, want unavailable", err)
	}
	if _, err := l.OpenStream(context.Background(), StreamCapture, "no-such-job", "x"); err == nil {
		t.Errorf("missing capture must be an error")
	}
}

func TestLocalJournalOps(t *testing.T) {
	p := testPaths(t)
	l := NewLocal(p, "self", LocalOptions{Logf: t.Logf})
	ctx := context.Background()
	if _, found, err := l.JournalGet(ctx, sid); err != nil || found {
		t.Fatalf("JournalGet before put: found=%v err=%v", found, err)
	}
	j := &job.Journal{ID: sid, SessionID: sid, Direction: "to", SourceHost: "laptop.example", DestHost: "big-storage.example"}
	j.Step("preflight").Status = job.Done
	if err := l.JournalPut(ctx, j); err != nil {
		t.Fatal(err)
	}
	got, found, err := l.JournalGet(ctx, sid)
	if err != nil || !found || got.Step("preflight").Status != job.Done || got.Dir != job.Dir(p.DataDir, sid) {
		t.Errorf("JournalGet: %+v found=%v err=%v", got, found, err)
	}
	if err := l.Record(ctx, sid, job.HistoryRecord{At: time.Now(), SessionID: sid, Outcome: "success"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(job.Dir(p.DataDir, sid), "history.jsonl")); err != nil {
		t.Errorf("history not written: %v", err)
	}
}

func TestLocalPlan03OpsAreExplicitStubs(t *testing.T) {
	l := NewLocal(testPaths(t), "self", LocalOptions{Logf: t.Logf})
	ctx := context.Background()
	checks := map[string]error{}
	// InventoryGit, GitDestState and GitAttach are implemented for real
	// (local_git.go, local_git_test.go) and no longer explicit stubs.
	// InventoryTmux, Capture, OpenWindow, StartClaude, ConfirmClaude,
	// ExitClaude, TypeCommand and PaneState are implemented for real
	// (local_tmux.go/local_claude.go, local_tmux_test.go/local_claude_test.go)
	// and no longer explicit stubs (Task 14).
	checks["RunPtyResume"] = l.RunPtyResume(ctx, session.ID(sid), "/home/bob/work", time.Second)
	for name, err := range checks {
		if !isUnavailable(err) {
			t.Errorf("%s: err = %v, want Error{Code: unavailable}", name, err)
		}
	}
}

func isUnavailable(err error) bool {
	var pe *Error
	return errors.As(err, &pe) && pe.Code == "unavailable"
}

func TestLocalResolveNotFoundMapsToProtocolError(t *testing.T) {
	l := NewLocal(testPaths(t), "self", LocalOptions{Logf: t.Logf})
	_, err := l.ResolveSession(context.Background(), session.Selector{ID: session.ID(sid)})
	if !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound (Serve maps it to not-found)", err)
	}
	if code := toError(err).Code; code != "not-found" {
		t.Errorf("toError code = %s", code)
	}
}

// --- Controller ruling 4: ServeStream regression tests wired to Local.OpenStream ---

// TestServeStreamLocalLogDeliversFullPayload proves the receive-direction
// (log) path: the client half-closes stdin *before* reading stdout (as
// Task 16's client must, per ServeStream's documented contract), and still
// gets the full payload with no truncation and no deadlock.
func TestServeStreamLocalLogDeliversFullPayload(t *testing.T) {
	p := testPaths(t)
	l := NewLocal(p, "self", LocalOptions{Logf: t.Logf})
	j, err := job.New(p.DataDir, sid)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat("log line to prove no truncation happens\n", 4096)
	if err := os.WriteFile(j.LogPath(), []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	serveDone := make(chan error, 1)
	go func() {
		err := ServeStream(context.Background(), StreamLog, sid, "s1", stdinR, stdoutW, l)
		// ServeStream itself never closes stdout — in production the
		// `remote stream` process exit is what EOFs the ssh channel to the
		// peer. An io.Pipe standing in for that channel needs the same
		// signal made explicit, or the reader below blocks forever.
		stdoutW.Close()
		serveDone <- err
	}()

	// The client has nothing to send: half-close stdin first, exactly as
	// documented in ServeStream (a receive-direction client that instead
	// waited to close stdin until after reading would deadlock).
	if err := stdinW.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := io.ReadAll(stdoutR)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if string(got) != want {
		t.Fatalf("log stream truncated or corrupted: got %d bytes, want %d", len(got), len(want))
	}

	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("ServeStream: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeStream did not return (deadlock)")
	}
}

// TestServeStreamLocalTarCompletesOnClientWriteThenClose proves the
// send-direction (tar) path: the client writes the payload then closes
// stdin, and ServeStream completes cleanly even though it also runs a
// concurrent "copy the stream to stdout" goroutine that has nothing to
// deliver (tarStream.Read returns io.EOF immediately, per its documented
// contract, so that goroutine can never block this from finishing).
func TestServeStreamLocalTarCompletesOnClientWriteThenClose(t *testing.T) {
	p := testPaths(t)
	l := NewLocal(p, "self", LocalOptions{Logf: t.Logf})
	m := sourceManifest(t, p)
	ctx := context.Background()

	st, err := l.ManifestDiff(ctx, m, sid)
	if err != nil {
		t.Fatal(err)
	}
	var payload bytes.Buffer
	if err := transfer.Send(ctx, m, transfer.Need(m, st), &payload, nil); err != nil {
		t.Fatal(err)
	}

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	serveDone := make(chan error, 1)
	go func() {
		err := ServeStream(ctx, StreamTar, sid, "s2", stdinR, stdoutW, l)
		// See the log test above: stand in for the process-exit EOF a real
		// `remote stream` invocation gives its peer.
		stdoutW.Close()
		serveDone <- err
	}()
	stdoutDone := make(chan struct{})
	go func() {
		io.Copy(io.Discard, stdoutR) // must return quickly (EOF), not block
		close(stdoutDone)
	}()

	if _, err := io.Copy(stdinW, &payload); err != nil {
		t.Fatal(err)
	}
	if err := stdinW.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("ServeStream: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeStream did not return (deadlock)")
	}
	select {
	case <-stdoutDone:
	case <-time.After(5 * time.Second):
		t.Fatal("stdout copy did not finish (tarStream.Read must not block)")
	}

	st, err = l.ManifestDiff(ctx, m, sid)
	if err != nil {
		t.Fatal(err)
	}
	if st[0] != transfer.StagedSame {
		t.Fatalf("after ServeStream: status = %v, want staged-same", st)
	}
}
