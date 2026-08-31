package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// copyFixture copies testdata/config/<rel> into a temp dir and returns the copy's path.
func copyFixture(t *testing.T, rel string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), filepath.Base(rel))
	mustWrite(t, dst, mustRead(t, filepath.Join("testdata/config", rel)))
	return dst
}

func TestReadIndexEntry(t *testing.T) {
	e, ok, err := ReadIndexEntry(fixtureProject, sidA)
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	want := &IndexEntry{SessionID: string(sidA),
		FullPath:  "/home/alice/.claude/projects/-home-alice-github-example-widget/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13.jsonl",
		FileMtime: 1756289762750, FirstPrompt: "Add a --verbose flag to the widget CLI", Summary: "Add verbose flag to widget CLI",
		MessageCount: 5, Created: "2026-08-27T10:15:30.123Z", Modified: "2026-08-27T10:16:02.750Z", GitBranch: "feature/teleport",
		ProjectPath: "/home/alice/github/example/widget"}
	if diff := cmp.Diff(want, e); diff != "" {
		t.Fatal(diff)
	}
	if _, ok, err := ReadIndexEntry(fixtureProject, ID("00000000-0000-4000-8000-000000000000")); err != nil || ok {
		t.Fatalf("unknown id: %v %v", ok, err)
	}
	if _, ok, err := ReadIndexEntry(t.TempDir(), sidA); err != nil || ok {
		t.Fatalf("missing index: %v %v", ok, err)
	}
}

func TestMergeIndexEntry(t *testing.T) {
	proj := filepath.Dir(copyFixture(t, "projects/-home-alice-github-example-widget/sessions-index.json"))
	e, _, _ := ReadIndexEntry(proj, sidA)
	e.FullPath = "/home/bob/.claude/projects/-home-bob-w/" + string(sidA) + ".jsonl"
	e.FileMtime = 42
	if err := MergeIndexEntry(proj, *e); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadIndexEntry(proj, sidA)
	if err != nil || !ok || got.FullPath != e.FullPath || got.FileMtime != 42 {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	if other, ok, _ := ReadIndexEntry(proj, sidB); !ok || other.FirstPrompt != "Explain the release process" {
		t.Fatalf("other entry damaged: %+v", other)
	}
	raw := mustRead(t, filepath.Join(proj, "sessions-index.json"))
	if !strings.Contains(raw, `"originalPath": "/home/alice/github/example/widget"`) || !strings.Contains(raw, `"version": 1`) {
		t.Fatalf("top-level fields lost:\n%s", raw)
	}
	if strings.Count(raw, string(sidA)) != 2 { // sessionId + fullPath, once each
		t.Fatalf("entry duplicated:\n%s", raw)
	}
	// creates the file when absent
	fresh := t.TempDir()
	if err := MergeIndexEntry(fresh, *e); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := ReadIndexEntry(fresh, sidA); !ok {
		t.Fatal("entry not written to a fresh index")
	}
}

func TestExtractAndAppendHistory(t *testing.T) {
	lines, err := ExtractHistory("testdata/config/history.jsonl", sidA)
	if err != nil || len(lines) != 2 {
		t.Fatalf("%d %v", len(lines), err)
	}
	if none, err := ExtractHistory(filepath.Join(t.TempDir(), "none"), sidA); err != nil || len(none) != 0 {
		t.Fatalf("missing history: %v %v", none, err)
	}
	dest := filepath.Join(t.TempDir(), "history.jsonl")
	mustWrite(t, dest, `{"display":"unrelated","pastedContents":{},"timestamp":1,"project":"/home/bob/p","sessionId":"00000000-0000-4000-8000-000000000000"}`) // no trailing newline
	added, err := AppendHistory(dest, lines)
	if err != nil || added != 2 {
		t.Fatalf("added %d %v", added, err)
	}
	added, err = AppendHistory(dest, lines)
	if err != nil || added != 0 {
		t.Fatalf("second append must dedupe: %d %v", added, err)
	}
	got := strings.Split(strings.TrimRight(mustRead(t, dest), "\n"), "\n")
	if len(got) != 3 || !strings.HasPrefix(got[0], `{"display":"unrelated"`) || !strings.Contains(got[2], "now run the tests") {
		t.Fatalf("%q", got)
	}
	for _, l := range got {
		if !json.Valid([]byte(l)) {
			t.Fatalf("corrupt line %q", l)
		}
	}
	// absent file is created
	fresh := filepath.Join(t.TempDir(), "h.jsonl")
	if added, err := AppendHistory(fresh, lines); err != nil || added != 2 {
		t.Fatalf("%d %v", added, err)
	}
}

func TestReadProjectEntry(t *testing.T) {
	e, ok, err := ReadProjectEntry("testdata/config/.claude.json", "/home/alice/github/example/widget")
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if e["hasTrustDialogAccepted"] != true || len(e["allowedTools"].([]any)) != 2 {
		t.Fatalf("%+v", e)
	}
	if _, ok, err := ReadProjectEntry("testdata/config/.claude.json", "/nope"); err != nil || ok {
		t.Fatalf("unknown cwd: %v %v", ok, err)
	}
	if _, ok, err := ReadProjectEntry(filepath.Join(t.TempDir(), "none.json"), "/x"); err != nil || ok {
		t.Fatalf("missing file: %v %v", ok, err)
	}
	if _, _, err := ReadProjectEntry("testdata/config/history.jsonl", "/x"); err == nil {
		t.Fatal("malformed global json must be an error")
	}
}

func TestAddProjectEntry(t *testing.T) {
	g := copyFixture(t, ".claude.json")
	e, _, _ := ReadProjectEntry(g, "/home/alice/github/example/widget")
	added, err := AddProjectEntry(g, "/home/bob/w", e)
	if err != nil || !added {
		t.Fatalf("%v %v", added, err)
	}
	if _, err := os.Stat(g + ".claude-teleport.bak"); err != nil {
		t.Fatal("backup missing")
	}
	added, err = AddProjectEntry(g, "/home/bob/w", e)
	if err != nil || added {
		t.Fatalf("present entry must be a no-op: %v %v", added, err)
	}
	raw := mustRead(t, g)
	for _, w := range []string{`"/home/bob/w"`, `"/home/alice/github/example/widget"`, `"emailAddress": "alice@example.com"`, `"numStartups": 12`, `"lastCost": 0.42`} {
		if !strings.Contains(raw, w) {
			t.Errorf("missing %s in\n%s", w, raw)
		}
	}
	// absent global file: created with just the projects map
	fresh := filepath.Join(t.TempDir(), ".claude.json")
	if added, err := AddProjectEntry(fresh, "/home/bob/w", e); err != nil || !added {
		t.Fatalf("%v %v", added, err)
	}
	if _, err := os.Stat(fresh + ".claude-teleport.bak"); err == nil {
		t.Fatal("no backup should be made when the file did not exist")
	}
}
