package session

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestForbidden(t *testing.T) {
	yes := []string{".credentials.json", ".claude.json", "settings.json", "settings.local.json", "sessions", "sessions/41234.json",
		"sessions/41234.0a1b2c3d.key", "plugins", "plugins/installed_plugins.json", "plugins/cache/x/y/1/.mcp.json", "foo/bar.key", "/sessions/1.json"}
	no := []string{"projects/-home-alice-x/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13.jsonl", "history.jsonl", "tasks/x/1.json", "file-history/x/y@v1", "keybindings.json"}
	for _, r := range yes {
		if !Forbidden(r) {
			t.Errorf("Forbidden(%q) = false", r)
		}
	}
	for _, r := range no {
		if Forbidden(r) {
			t.Errorf("Forbidden(%q) = true", r)
		}
	}
}

func TestInventoryFiles(t *testing.T) {
	s, err := Load(fixturePaths(), sidA, nil)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := InventoryFiles(s)
	if err != nil {
		t.Fatal(err)
	}
	var rels []string
	for _, f := range inv.Files {
		if f.Category != CatSession || f.Root != "testdata/config" {
			t.Errorf("entry %+v: wrong root/category", f)
		}
		if Forbidden(f.Rel) {
			t.Errorf("forbidden path in inventory: %s", f.Rel)
		}
		if !f.Mode.IsDir() {
			rels = append(rels, f.Rel)
		}
	}
	sort.Strings(rels)
	const proj = "projects/-home-alice-github-example-widget/"
	want := []string{
		"file-history/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13/0a1b2c3d4e5f60718293a4b5c6d7e8f9@v1",
		proj + "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13.jsonl",
		proj + "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13/subagents/agent-0f8e7d6c.jsonl",
		proj + "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13/subagents/agent-0f8e7d6c.meta.json",
		proj + "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13/tool-results/toolu_01Ab3.txt",
		"session-env/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13/env.json",
		"tasks/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13/1.json",
		"todos/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13-agent-0f8e7d6c.json",
	}
	if strings.Join(rels, "\n") != strings.Join(want, "\n") {
		t.Fatalf("files:\n%s\nwant:\n%s", strings.Join(rels, "\n"), strings.Join(want, "\n"))
	}
	for _, f := range inv.Files {
		wantRewrite := strings.HasSuffix(f.Rel, ".json") || strings.HasSuffix(f.Rel, ".jsonl")
		if !f.Mode.IsDir() && f.Rewrite != wantRewrite {
			t.Errorf("%s: Rewrite=%v", f.Rel, f.Rewrite)
		}
		if f.Rel == proj+"3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13.jsonl" && f.Path() != filepath.Join("testdata/config", f.Rel) {
			t.Errorf("Path() = %s", f.Path())
		}
	}
	if len(inv.Skipped) != 1 || !strings.HasSuffix(inv.Skipped[0].Path, "/.lock") {
		t.Fatalf("skipped = %+v", inv.Skipped)
	}
	if len(inv.Memory) != 1 || inv.Memory[0].Rel != proj+"memory/MEMORY.md" {
		t.Fatalf("memory = %+v", inv.Memory)
	}
	// the other session's transcript and the index are not ours to move
	for _, f := range inv.Files {
		if strings.Contains(f.Rel, string(sidB)) || strings.HasSuffix(f.Rel, "sessions-index.json") {
			t.Errorf("unexpected %s", f.Rel)
		}
	}
}

// A config dir containing every forbidden path, plus symlinks from session
// dirs pointing at them: nothing forbidden may come out, symlinks are
// recorded as symlinks (never followed), fifos are skipped and reported.
func TestInventoryNeverReturnsForbidden(t *testing.T) {
	dir := t.TempDir()
	p := NewPaths("/home/alice", dir, dir, true)
	const sid = "deadbeef-0000-4000-8000-000000000001"
	proj := p.ProjectDir("/home/alice/x")
	mustWrite(t, proj+"/"+sid+".jsonl", `{"type":"user","cwd":"/home/alice/x","sessionId":"`+sid+`","message":{"content":"hi"}}`+"\n")
	for _, f := range []string{".credentials.json", ".claude.json", "settings.json", "sessions/1.json", "sessions/1.ab.key", "plugins/installed_plugins.json"} {
		mustWrite(t, filepath.Join(dir, f), "{}")
	}
	mustMkdir(t, filepath.Join(dir, "tasks", sid))
	if err := os.Symlink(filepath.Join(dir, ".credentials.json"), filepath.Join(dir, "tasks", sid, "creds")); err != nil {
		t.Fatal(err)
	}
	if err := syscallMkfifo(filepath.Join(dir, "tasks", sid, "pipe")); err != nil {
		t.Fatal(err)
	}
	s, err := Load(p, ID(sid), nil)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := InventoryFiles(s)
	if err != nil {
		t.Fatal(err)
	}
	var sawLink bool
	for _, f := range inv.Files {
		if Forbidden(f.Rel) || strings.Contains(f.Rel, "credentials") && f.Symlink == "" {
			t.Errorf("forbidden content leaked: %+v", f)
		}
		if f.Symlink != "" {
			sawLink = true
			if f.Rel != "tasks/"+sid+"/creds" || f.Size != 0 {
				t.Errorf("symlink entry %+v", f)
			}
		}
	}
	if !sawLink {
		t.Fatal("symlink must be recorded as a symlink entry")
	}
	if len(inv.Skipped) != 1 || !strings.Contains(inv.Skipped[0].Reason, "fifo") {
		t.Fatalf("skipped = %+v", inv.Skipped)
	}
}
