package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/session"
)

const sid = "7a3f9c1e-2b4d-4e6f-8a1b-3c5d7e9f0a2b"

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// sourceTree builds a minimal source config dir under a sandboxed "home"
// that stands in for /home/alice (session.PathMap.ApplyPath only rewrites a
// literal prefix, and tests cannot write into a real /home/alice, so the
// embedded JSON content below uses this same sandboxed home rather than an
// unrelated hardcoded "/home/alice" that a path map could never match here).
func sourceTree(t *testing.T) (cfg, home string, files []session.FileEntry) {
	t.Helper()
	home = filepath.Join(t.TempDir(), "home", "alice")
	cfg = filepath.Join(home, ".claude")
	proj := "projects/-home-alice-work"
	writeFile(t, filepath.Join(cfg, proj, sid+".jsonl"),
		`{"type":"user","cwd":"`+home+`/work","sessionId":"`+sid+`"}`+"\n"+
			`{"type":"assistant","cwd":"`+home+`/work","n":1.50}`+"\n")
	writeFile(t, filepath.Join(cfg, proj, sid, "subagents", "agent-1.jsonl"), `{"cwd":"`+home+`/work"}`+"\n")
	writeFile(t, filepath.Join(cfg, "todos", sid+".json"), `{"path":"`+home+`/work/x"}`)
	os.Symlink("../"+sid+".jsonl", filepath.Join(cfg, proj, "link.jsonl"))
	mt := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	mk := func(rel string, mode os.FileMode, size int64, rewrite bool, link string) session.FileEntry {
		return session.FileEntry{Root: cfg, Rel: rel, Category: session.CatSession, Size: size, Mode: mode, ModTime: mt, Rewrite: rewrite, Symlink: link}
	}
	files = []session.FileEntry{
		mk(proj, os.ModeDir|0o700, 0, false, ""),
		mk(proj+"/"+sid+".jsonl", 0o600, 0, true, ""),
		mk(proj+"/"+sid+"/subagents/agent-1.jsonl", 0o600, 0, true, ""),
		mk("todos/"+sid+".json", 0o600, 0, true, ""),
		mk(proj+"/link.jsonl", os.ModeSymlink|0o777, 0, false, "../"+sid+".jsonl"),
	}
	return cfg, home, files
}

// bobHome mirrors home ("/home/alice"-sandbox) but with "bob" substituted
// for "alice" as the final path segment, for use as a session.Mapping To.
func bobHome(home string) string {
	return filepath.Join(filepath.Dir(home), "bob")
}

func TestBuildHashesRewrittenContent(t *testing.T) {
	cfg, home, files := sourceTree(t)
	bob := bobHome(home)
	pm := session.NewPathMap(session.Mapping{From: home, To: bob})
	m, err := Build(context.Background(), sid, session.ID(sid), "laptop.example", "big-storage.example", files, pm)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != 1 || m.JobID != sid || m.SessionID != sid || m.SourceHost != "laptop.example" || m.DestHost != "big-storage.example" {
		t.Errorf("header: %+v", m)
	}
	if len(m.Entries) != 5 {
		t.Fatalf("entries = %d", len(m.Entries))
	}
	tr := m.Entries[1]
	// session.RewriteJSONL decodes each line into a plain map and re-encodes
	// it; encoding/json sorts map keys alphabetically, so the rewritten
	// output reorders "type","cwd",... to "cwd",...,"type".
	wantContent := `{"cwd":"` + bob + `/work","sessionId":"` + sid + `","type":"user"}` + "\n" +
		`{"cwd":"` + bob + `/work","n":1.50,"type":"assistant"}` + "\n"
	if tr.SHA256 != sha(wantContent) || tr.Size != int64(len(wantContent)) {
		t.Errorf("transcript hash/size are not of the rewritten content: %+v", tr)
	}
	// FFAllowed is keyed off the session id: transcript, sidecar dir, and
	// todos/<sid>.json are all part of THIS session (controller ruling 1).
	if !tr.FFAllowed || !m.Entries[2].FFAllowed || !m.Entries[3].FFAllowed {
		t.Errorf("FFAllowed: transcript=%v sidecar=%v todo=%v", tr.FFAllowed, m.Entries[2].FFAllowed, m.Entries[3].FFAllowed)
	}
	wantDst := strings.Replace(cfg, home+"/", bob+"/", 1)
	if tr.Dst != filepath.Join(wantDst, "projects/-home-alice-work", sid+".jsonl") || tr.Src != files[1].Path() {
		t.Errorf("paths: src=%s dst=%s", tr.Src, tr.Dst)
	}
	if m.Entries[0].SHA256 != "" || m.Entries[4].SHA256 != "" || m.Entries[4].Symlink != "../"+sid+".jsonl" {
		t.Errorf("dir/symlink entries must have no hash: %+v %+v", m.Entries[0], m.Entries[4])
	}
	for i, e := range m.Entries {
		if e.ID != i {
			t.Errorf("entry %d has id %d", i, e.ID)
		}
	}
	if _, ok := m.ByID(4); !ok {
		t.Errorf("ByID(4) missing")
	}
	if _, ok := m.ByID(99); ok {
		t.Errorf("ByID(99) should be absent")
	}
}

// TestBuildProjectDirMappingRecomputesMungedComponent covers spec §7.2
// (ruling R-P3-18a): a plain Home-prefix mapping cannot rewrite the
// Munge()'d project-directory path component embedded in a CatSession
// entry's Root (Munge flattens the whole cwd into one path segment, so a
// leading-prefix rewrite of Root never reaches inside it). When the caller
// also supplies a mapping FROM the source project directory TO the
// destination's own (recomputed) munged project directory, NewPathMap's
// longest-prefix-first ordering makes that more specific mapping win, and
// Dst carries the destination's munged spelling instead of the source's.
func TestBuildProjectDirMappingRecomputesMungedComponent(t *testing.T) {
	cfg, home, files := sourceTree(t)
	bob := bobHome(home)
	bobCfg := filepath.Join(bob, ".claude")
	srcProj := filepath.Join(cfg, "projects/-home-alice-work")
	dstProj := filepath.Join(bobCfg, "projects/-home-bob-elsewhere") // recomputed from the dest cwd, not a mechanical text substitution of srcProj
	pm := session.NewPathMap(
		session.Mapping{From: home, To: bob},
		session.Mapping{From: srcProj, To: dstProj},
	)
	m, err := Build(context.Background(), sid, session.ID(sid), "a", "b", files, pm)
	if err != nil {
		t.Fatal(err)
	}
	tr := m.Entries[1]
	wantDst := filepath.Join(dstProj, sid+".jsonl")
	if tr.Dst != wantDst {
		t.Errorf("Dst = %q, want dest munged project dir %q", tr.Dst, wantDst)
	}
	dir := m.Entries[0]
	if dir.Dst != dstProj {
		t.Errorf("dir Dst = %q, want %q", dir.Dst, dstProj)
	}
}

func TestBuildRawHashWithoutRewrite(t *testing.T) {
	cfg, _, files := sourceTree(t)
	files[1].Rewrite = false
	m, err := Build(context.Background(), sid, session.ID(sid), "a", "b", files[1:2], session.NewPathMap())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(cfg, "projects/-home-alice-work", sid+".jsonl"))
	if m.Entries[0].SHA256 != sha(string(raw)) || m.Entries[0].Size != int64(len(raw)) {
		t.Errorf("raw hash mismatch")
	}
	if m.Entries[0].Dst != m.Entries[0].Src {
		t.Errorf("empty path map must keep the path")
	}
}

// TestBuildStatFallbackForZeroModeModTime covers a Task 10 gap (controller
// ruling 2): a FileEntry built without a stat (Mode==0, zero ModTime — as
// Plan 03's capture entry does) still ends up with the real file's Mode and
// ModTime in the manifest, via Build's os.Stat fallback.
func TestBuildStatFallbackForZeroModeModTime(t *testing.T) {
	cfg, _, files := sourceTree(t)
	f := files[1] // the transcript, a real regular file
	f.Mode = 0
	f.ModTime = time.Time{}
	m, err := Build(context.Background(), sid, session.ID(sid), "a", "b", []session.FileEntry{f}, session.NewPathMap())
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(filepath.Join(cfg, "projects/-home-alice-work", sid+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	got := m.Entries[0]
	if got.Mode != uint32(want.Mode()) || !got.ModTime.Equal(want.ModTime()) {
		t.Errorf("stat fallback: Mode=%v (want %v) ModTime=%v (want %v)", got.Mode, uint32(want.Mode()), got.ModTime, want.ModTime())
	}
}

func TestBuildRefusesForbiddenPaths(t *testing.T) {
	cfg, _, _ := sourceTree(t)
	forbidden := []string{".credentials.json", "sessions/12345.json", "sessions/12345.abcd.key", "settings.json", "plugins/installed_plugins.json", ".claude.json"}
	var files []session.FileEntry
	for _, rel := range forbidden {
		writeFile(t, filepath.Join(cfg, rel), "{}")
		files = append(files, session.FileEntry{Root: cfg, Rel: rel, Category: session.CatSession, Mode: 0o600})
	}
	_, err := Build(context.Background(), sid, session.ID(sid), "a", "b", files, session.NewPathMap())
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
	for _, rel := range forbidden {
		if !strings.Contains(err.Error(), rel) {
			t.Errorf("error must list %s: %v", rel, err)
		}
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	_, home, files := sourceTree(t)
	bob := bobHome(home)
	m, err := Build(context.Background(), sid, session.ID(sid), "a", "b", files, session.NewPathMap(session.Mapping{From: home, To: bob}))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "manifest.json")
	if err := m.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != len(m.Entries) || got.Entries[1].SHA256 != m.Entries[1].SHA256 || got.PathMap.ApplyPath(home+"/x") != bob+"/x" {
		t.Errorf("round trip lost data: %+v", got)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Errorf("Load missing must error")
	}
}

// TestFFAllowedKeyedOnSessionID covers controller ruling 1: FFAllowed keys
// off the session id itself (a "/<sid>." or "/<sid>/" segment, or a sidecar
// dir named by the sid), not a projects/-prefix heuristic. It must recognise
// todos/<sid>.json, and must NOT match another session's file that merely
// lives in the same project directory.
func TestFFAllowedKeyedOnSessionID(t *testing.T) {
	const otherSid = "0b1c2d3e-4f5a-4b6c-8d9e-0f1a2b3c4d5e"
	cfg := filepath.Join(t.TempDir(), "home", "alice", ".claude")
	proj := "projects/-home-alice-work"
	writeFile(t, filepath.Join(cfg, proj, sid+".jsonl"), `{}`+"\n")
	writeFile(t, filepath.Join(cfg, "todos", sid+".json"), `{}`)
	writeFile(t, filepath.Join(cfg, proj, otherSid+".jsonl"), `{}`+"\n")
	mt := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	files := []session.FileEntry{
		{Root: cfg, Rel: proj + "/" + sid + ".jsonl", Category: session.CatSession, Mode: 0o600, ModTime: mt},
		{Root: cfg, Rel: "todos/" + sid + ".json", Category: session.CatSession, Mode: 0o600, ModTime: mt},
		{Root: cfg, Rel: proj + "/" + otherSid + ".jsonl", Category: session.CatSession, Mode: 0o600, ModTime: mt},
	}
	m, err := Build(context.Background(), sid, session.ID(sid), "a", "b", files, session.NewPathMap())
	if err != nil {
		t.Fatal(err)
	}
	if !m.Entries[0].FFAllowed {
		t.Errorf("transcript must be FFAllowed")
	}
	if !m.Entries[1].FFAllowed {
		t.Errorf("todos/<sid>.json must be FFAllowed")
	}
	if m.Entries[2].FFAllowed {
		t.Errorf("another session's file in the same project dir must NOT be FFAllowed: %+v", m.Entries[2])
	}
}
