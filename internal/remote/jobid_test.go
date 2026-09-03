package remote

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

// hostileIDs is ruling R-P3-23n's fixture: a job id arrives from the far
// side of an ssh pipe and is joined verbatim into jobs/<id> and
// staging/<id>, so these are the shapes that must never reach a
// filesystem call. escapes is the traversal that really does resolve onto
// the canary file below (the test asserts that, so it cannot rot into a
// harmless string), inspectEscape is the same escape wearing the
// "inspect-" prefix remove-job used to trust on its own.
type hostileIDs struct {
	root, canary          string
	escape, inspectEscape string
	all                   []string
}

func newHostileIDs(t *testing.T, p session.Paths) hostileIDs {
	t.Helper()
	// The temp root ABOVE the sandboxed home: a real teleport data dir
	// lives deep under $HOME, and the point of the traversal is to get
	// out of it entirely.
	root := filepath.Dir(filepath.Dir(p.Home))
	canary := filepath.Join(root, "canary.txt")
	if err := os.WriteFile(canary, []byte("do not touch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rel := func(from string) string {
		r, err := filepath.Rel(from, canary)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	jobs := filepath.Join(p.DataDir, "jobs")
	h := hostileIDs{
		root:          root,
		canary:        canary,
		escape:        rel(jobs),
		inspectEscape: "inspect-x/" + rel(filepath.Join(jobs, "inspect-x")),
	}
	// Proof the fixture is a real exploit and not just an odd string:
	// both ids resolve, through job.Dir, onto the canary itself.
	if got := job.Dir(p.DataDir, h.escape); got != canary {
		t.Fatalf("escape id does not resolve onto the canary: %s != %s", got, canary)
	}
	if got := job.Dir(p.DataDir, h.inspectEscape); got != canary {
		t.Fatalf("inspect- escape id does not resolve onto the canary: %s != %s", got, canary)
	}
	h.all = []string{h.escape, h.inspectEscape, "", "..", "/etc/passwd", "a\x00b", `..\..\x`}
	return h
}

// snapshotTree records every path (and file content) under root, so a
// refused op can be shown to have touched nothing at all — neither the
// canary outside the data dir nor the data dir's own contents.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			out[path] = "<dir>"
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[path] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func assertTreeUnchanged(t *testing.T, root string, before map[string]string) {
	t.Helper()
	after := snapshotTree(t, root)
	for path, want := range before {
		got, ok := after[path]
		if !ok {
			t.Errorf("%s was removed by a refused op", path)
			continue
		}
		if got != want {
			t.Errorf("%s changed: %q -> %q", path, want, got)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("%s was created by a refused op", path)
		}
	}
}

// jobIDOps enumerates every dispatched op whose args carry a job id that
// this host turns into a path. args builds a wire-shaped value for one id.
func jobIDOps() []struct {
	op   string
	args func(id string) any
} {
	ref := &session.TmuxRef{SocketPath: "/nonexistent/tmux", PaneID: "%1"}
	m := func() *transfer.Manifest { return &transfer.Manifest{Version: 1} }
	return []struct {
		op   string
		args func(id string) any
	}{
		{OpManifestDiff, func(id string) any { return ManifestDiffArgs{Manifest: m(), JobID: id} }},
		{OpInstallExtras, func(id string) any { return InstallExtrasArgs{JobID: id} }},
		{OpInstall, func(id string) any { return InstallArgs{Manifest: m(), JobID: id} }},
		{OpGitAttach, func(id string) any {
			return GitAttachArgs{Plan: &gitx.Plan{Mode: gitx.ModeNotRepo, PackEntryID: gitx.NoEntry, IndexEntryID: gitx.NoEntry}, JobID: id}
		}},
		{OpCapture, func(id string) any { return CaptureArgs{Ref: ref, JobID: id} }},
		{OpStartClaude, func(id string) any {
			return StartClaudeArgs{Ref: ref, ID: session.ID(sid), JobID: id, Argv: []string{"claude"}}
		}},
		{OpJournalGet, func(id string) any { return JournalGetArgs{JobID: id} }},
		{OpJournalPut, func(id string) any { return JournalPutArgs{Journal: &job.Journal{ID: id, SessionID: id}} }},
		{OpRecord, func(id string) any { return RecordArgs{JobID: id} }},
		{OpBuildManifest, func(id string) any { return buildManifestArgs{JobID: id, ID: session.ID(sid)} }},
		{OpCleanup, func(id string) any { return cleanupArgs{JobID: id} }},
		{OpRemoveJob, func(id string) any { return removeJobArgs{JobID: id} }},
	}
}

// TestServeRejectsHostileJobIDs is ruling R-P3-23n at the wire boundary:
// EVERY op carrying a job id must refuse a traversal/absolute/empty/NUL id
// with a protocol error before its handler runs, and the refusal must
// leave the host's filesystem — the canary outside the data dir included —
// byte for byte unchanged.
func TestServeRejectsHostileJobIDs(t *testing.T) {
	p := testPaths(t)
	if err := os.MkdirAll(filepath.Join(p.DataDir, "jobs"), 0o700); err != nil {
		t.Fatal(err)
	}
	h := newHostileIDs(t, p)
	ep := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc"})
	for _, o := range jobIDOps() {
		for _, id := range h.all {
			t.Run(o.op+"/"+strings.NewReplacer("/", "_", "\x00", "NUL", `\`, "bs").Replace(id), func(t *testing.T) {
				// Snapshotted per CALL, not once around the whole table: a
				// single before/after pair would let one op's stray write
				// be cancelled out by another op's stray delete, and would
				// not say which op did it.
				before := snapshotTree(t, h.root)
				e := callOp(t, ep, o.op, o.args(id), nil)
				if e == nil {
					t.Fatalf("%s accepted job id %q", o.op, id)
				}
				if e.Code != "usage" {
					t.Errorf("%s job id %q: error code = %q, want usage (%s)", o.op, id, e.Code, e.Message)
				}
				if !strings.Contains(e.Message, "job id") {
					t.Errorf("%s job id %q: message %q does not mention the job id", o.op, id, e.Message)
				}
				assertTreeUnchanged(t, h.root, before)
				if _, err := os.Stat(h.canary); err != nil {
					t.Fatalf("the canary outside the data dir must survive %s: %v", o.op, err)
				}
			})
		}
	}
}

// TestLocalRejectsHostileJobIDs is the defense-in-depth half: Local is the
// thing that actually touches the disk, so it validates for itself — a
// future caller reaching a Local method by some path other than the wire
// dispatch (ServeStream, a chained endpoint, this process's own code)
// cannot get a traversal id past it either.
func TestLocalRejectsHostileJobIDs(t *testing.T) {
	p := testPaths(t)
	if err := os.MkdirAll(filepath.Join(p.DataDir, "jobs"), 0o700); err != nil {
		t.Fatal(err)
	}
	h := newHostileIDs(t, p)
	l := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc"})
	ctx := context.Background()
	ref := &session.TmuxRef{SocketPath: "/nonexistent/tmux", PaneID: "%1"}
	plan := &gitx.Plan{Mode: gitx.ModeNotRepo, PackEntryID: gitx.NoEntry, IndexEntryID: gitx.NoEntry}
	calls := map[string]func(id string) error{
		"BuildManifest": func(id string) error {
			_, err := l.BuildManifest(ctx, id, session.ID(sid), "laptop.example", "dest.private", nil, session.PathMap{})
			return err
		},
		"ManifestDiff": func(id string) error {
			_, err := l.ManifestDiff(ctx, &transfer.Manifest{Version: 1}, id)
			return err
		},
		"PutInstallExtras": func(id string) error {
			return l.PutInstallExtras(ctx, id, transfer.InstallExtras{})
		},
		"Install": func(id string) error {
			_, err := l.Install(ctx, &transfer.Manifest{Version: 1}, id)
			return err
		},
		"Cleanup":   func(id string) error { return l.Cleanup(ctx, id) },
		"RemoveJob": func(id string) error { return l.RemoveJob(ctx, id) },
		"GitAttach": func(id string) error { return l.GitAttach(ctx, plan, id) },
		"Capture":   func(id string) error { return l.Capture(ctx, ref, id) },
		"JournalGet": func(id string) error {
			_, _, err := l.JournalGet(ctx, id)
			return err
		},
		"JournalPut": func(id string) error { return l.JournalPut(ctx, &job.Journal{ID: id, SessionID: id}) },
		"Record":     func(id string) error { return l.Record(ctx, id, job.HistoryRecord{}) },
		"OpenStream": func(id string) error {
			_, err := l.OpenStream(ctx, StreamTar, id, "send:0")
			return err
		},
		"ServeStream": func(id string) error {
			return ServeStream(ctx, StreamTar, id, "send:0", strings.NewReader(""), io.Discard, l)
		},
	}
	for name, call := range calls {
		for _, id := range h.all {
			// Per call, for the same reason as the wire test above.
			before := snapshotTree(t, h.root)
			err := call(id)
			if err == nil {
				t.Errorf("Local.%s accepted job id %q", name, id)
				continue
			}
			var pe *Error
			if !errors.As(err, &pe) || pe.Code != "usage" || !strings.Contains(pe.Message, "job id") {
				t.Errorf("Local.%s job id %q: err = %v, want a usage error naming the job id", name, id, err)
			}
			assertTreeUnchanged(t, h.root, before)
		}
	}
}
