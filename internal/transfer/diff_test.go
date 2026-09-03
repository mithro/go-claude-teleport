package transfer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// destManifest fabricates a manifest whose Dst paths live under dest.
func destManifest(dest string) *Manifest {
	proj := filepath.Join(dest, ".claude", "projects", "-home-bob-work")
	tr := "line1\nline2\n"
	m := &Manifest{Version: 1, JobID: sid, SessionID: sid}
	m.Entries = []Entry{
		{ID: 0, Category: session.CatSession, Dst: proj, Mode: uint32(os.ModeDir | 0o700)},
		{ID: 1, Category: session.CatSession, Dst: filepath.Join(proj, sid+".jsonl"), Size: int64(len(tr)), Mode: 0o600, SHA256: sha(tr), FFAllowed: true},
		{ID: 2, Category: session.CatSession, Dst: filepath.Join(proj, "other.json"), Size: 2, Mode: 0o600, SHA256: sha("{}")},
		{ID: 3, Category: session.CatSession, Dst: filepath.Join(proj, "link"), Mode: uint32(os.ModeSymlink | 0o777), Symlink: "target"},
	}
	return m
}

func TestDiffStatuses(t *testing.T) {
	dest := t.TempDir()
	staging := filepath.Join(t.TempDir(), "staging")
	os.MkdirAll(staging, 0o700)
	m := destManifest(dest)

	st, err := Diff(context.Background(), m, staging)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]Status{0: Absent, 1: Absent, 2: Absent, 3: Absent}
	if diff := cmp.Diff(want, st); diff != "" {
		t.Errorf("empty dest (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]int{0, 1, 2, 3}, Need(m, st)); diff != "" {
		t.Errorf("Need (-want +got):\n%s", diff)
	}

	// present-same dir, present-same file, symlink same; a staged copy of 1; a .part remnant for 2
	os.MkdirAll(m.Entries[0].Dst, 0o700)
	writeFile(t, m.Entries[2].Dst, "{}")
	os.Symlink("target", m.Entries[3].Dst)
	writeFile(t, StagedPath(staging, 1), "line1\nline2\n")
	writeFile(t, StagedPath(staging, 2)+".part", "{")
	st, err = Diff(context.Background(), m, staging)
	if err != nil {
		t.Fatal(err)
	}
	want = map[int]Status{0: PresentSame, 1: StagedSame, 2: PresentSame, 3: PresentSame}
	if diff := cmp.Diff(want, st); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	if _, err := os.Stat(StagedPath(staging, 2) + ".part"); !os.IsNotExist(err) {
		t.Errorf(".part remnant must be deleted by Diff")
	}
	if got := Need(m, st); len(got) != 0 {
		t.Errorf("Need = %v, want none", got)
	}

	// wrong-size staged copy -> staged-mismatch and removed
	os.Remove(m.Entries[2].Dst)
	writeFile(t, StagedPath(staging, 2), "{}}}")
	st, _ = Diff(context.Background(), m, staging)
	if st[2] != StagedMismatch {
		t.Errorf("wrong-size staged = %s", st[2])
	}
	if _, err := os.Stat(StagedPath(staging, 2)); !os.IsNotExist(err) {
		t.Errorf("mismatched staged file must be deleted")
	}
	if diff := cmp.Diff([]int{2}, Need(m, st)); diff != "" {
		t.Errorf("Need (-want +got):\n%s", diff)
	}
}

// TestPendingExcludesFFCandidate pins the bug an orchestrator e2e test
// (a re-teleport back to a host that already holds an older copy of this
// session's own transcript) found: verifyTransfer/runTransfer used Need as
// a "the transfer step is done" oracle, but Need deliberately keeps
// listing an already-staged ff-candidate (its own doc comment: "so a
// resend after a crash is never skipped") — so a fast-forward transfer
// could never converge, forever reporting the just-received file "still
// missing". Pending is the correct completeness check: it excludes
// ff-candidate (and present-different-FFAllowed, and present/staged-same),
// since all of those already have a correctly staged copy for the later
// install step to use.
func TestPendingExcludesFFCandidate(t *testing.T) {
	dest := t.TempDir()
	staging := filepath.Join(t.TempDir(), "staging")
	m := destManifest(dest)
	writeFile(t, m.Entries[1].Dst, "line1\n")              // older copy of the FFAllowed transcript
	writeFile(t, StagedPath(staging, 1), "line1\nline2\n") // the just-received, longer copy
	st, err := Diff(context.Background(), m, staging)
	if err != nil {
		t.Fatal(err)
	}
	if st[1] != FFCandidate {
		t.Fatalf("entry 1 = %s, want ff-candidate", st[1])
	}
	if got := Need(m, st); len(got) == 0 || got[0] != 1 {
		t.Fatalf("Need = %v, want it to still list the ff-candidate (over-inclusive by design)", got)
	}
	pending := Pending(m, st)
	for _, id := range pending {
		if id == 1 {
			t.Fatalf("Pending = %v, must not list an already-staged ff-candidate", pending)
		}
	}
	// entry 0 (the project dir) exists as a side effect of writeFile
	// MkdirAll-ing entry 1's parent -> present-same, not pending. Entries 2
	// (other.json) and 3 (symlink) are genuinely absent -> still pending.
	if diff := cmp.Diff([]int{2, 3}, pending); diff != "" {
		t.Errorf("Pending (-want +got):\n%s", diff)
	}
}

func TestDiffFastForwardAndCollision(t *testing.T) {
	dest := t.TempDir()
	staging := filepath.Join(t.TempDir(), "staging")
	m := destManifest(dest)

	// transcript on dest differs from the incoming one, FFAllowed, no staged
	// copy yet -> ff-candidate unconditionally (no size heuristic, controller
	// ruling 1); an unrelated (non-FFAllowed) same-size-but-different file is
	// present-different regardless of size.
	writeFile(t, m.Entries[1].Dst, "line1\n")
	writeFile(t, m.Entries[2].Dst, "[]") // same size, different content -> present-different
	st, err := Diff(context.Background(), m, staging)
	if err != nil {
		t.Fatal(err)
	}
	if st[1] != FFCandidate || st[2] != PresentDifferent {
		t.Errorf("statuses: %v", st)
	}
	blk := Blocking(m, st, false)
	if len(blk) != 1 || blk[0].ID != 2 {
		t.Errorf("Blocking = %v, want entry 2 only (ff-candidate is allowed)", blk)
	}
	// entry 3 (the symlink) is never created on dest in this test, so it
	// stays absent and needed throughout.
	if diff := cmp.Diff([]int{1, 3}, Need(m, st)); diff != "" {
		t.Errorf("Need (-want +got):\n%s", diff)
	}

	// once staged, the prefix check is exact: a non-prefix becomes present-different
	writeFile(t, StagedPath(staging, 1), "line1\nline2\n")
	writeFile(t, m.Entries[1].Dst, "lineX\n")
	st, _ = Diff(context.Background(), m, staging)
	if st[1] != PresentDifferent {
		t.Errorf("non-prefix same-session transcript = %s, want present-different", st[1])
	}
	if blk := Blocking(m, st, false); len(blk) != 2 {
		t.Errorf("Blocking without force = %v", blk)
	}
	// --force lifts the block for FFAllowed entries only
	blk = Blocking(m, st, true)
	if len(blk) != 1 || blk[0].ID != 2 {
		t.Errorf("Blocking with force = %v, want only the unrelated file", blk)
	}
	// entry 1 (FFAllowed, present-different) is needed; entry 2 (unrelated)
	// never is; entry 3 stays absent and needed as above.
	if diff := cmp.Diff([]int{1, 3}, Need(m, st)); diff != "" {
		t.Errorf("Need with forced present-different (-want +got):\n%s", diff)
	}

	// dir vs file collision
	writeFile(t, filepath.Join(dest, "x"), "")
	m.Entries[0].Dst = filepath.Join(dest, "x")
	st, _ = Diff(context.Background(), m, staging)
	if st[0] != PresentDifferent {
		t.Errorf("file where dir expected = %s", st[0])
	}
}

// TestDiffFFCandidateRecordReflow covers controller ruling 1: once a staged
// copy of a .jsonl entry exists, the authoritative check is
// session.IsRecordPrefix, which tolerates a destination transcript that has
// been byte-reflowed (different key order/whitespace) as long as its records
// are, in order, record-equal to a prefix of the staged file.
func TestDiffFFCandidateRecordReflow(t *testing.T) {
	dest := t.TempDir()
	staging := filepath.Join(t.TempDir(), "staging")
	proj := filepath.Join(dest, ".claude", "projects", "-home-bob-work")
	rec1 := `{"type":"user","cwd":"/home/bob/work"}`
	rec2 := `{"type":"assistant","n":1.5}`
	full := rec1 + "\n" + rec2 + "\n"
	m := &Manifest{Version: 1, JobID: sid, SessionID: sid}
	m.Entries = []Entry{
		{ID: 0, Category: session.CatSession, Dst: filepath.Join(proj, sid+".jsonl"), Size: int64(len(full)), Mode: 0o600, SHA256: sha(full), FFAllowed: true},
	}
	// dest has only the first record, re-encoded with different key order/whitespace
	reflowed := `{ "cwd": "/home/bob/work",   "type": "user" }` + "\n"
	writeFile(t, m.Entries[0].Dst, reflowed)
	writeFile(t, StagedPath(staging, 0), full)

	st, err := Diff(context.Background(), m, staging)
	if err != nil {
		t.Fatal(err)
	}
	if st[0] != FFCandidate {
		t.Errorf("byte-reflowed record-equal prefix = %s, want ff-candidate", st[0])
	}
}

// TestDiffPropagatesNonENOENTStagedErrors covers a fix-round-1 finding:
// stagedState's dir/symlink branches, and Diff's symlink comparison, must
// only treat os.ErrNotExist as "not staged"/"absent" — any other error (a
// permission failure here) must propagate wrapped with the offending path,
// not be silently swallowed as "not staged".
func TestDiffPropagatesNonENOENTStagedErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not gate reads")
	}
	dest := t.TempDir()
	staging := t.TempDir()
	m := &Manifest{Version: 1, JobID: sid, SessionID: sid}
	m.Entries = []Entry{
		{ID: 0, Category: session.CatSession, Dst: filepath.Join(dest, "link"), Mode: uint32(os.ModeSymlink | 0o777), Symlink: "target"},
	}
	staged := StagedPath(staging, 0) + ".symlink"
	writeFile(t, staged, "target")
	if err := os.Chmod(staged, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(staged, 0o600) // best-effort: let TempDir cleanup remove it either way

	_, err := Diff(context.Background(), m, staging)
	if err == nil {
		t.Fatal("Diff must propagate a non-ENOENT staged-symlink read error, not swallow it")
	}
	if !strings.Contains(err.Error(), staged) {
		t.Errorf("error must name the path %s: %v", staged, err)
	}
}
