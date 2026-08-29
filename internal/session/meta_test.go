package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const (
	fixtureProject = "testdata/config/projects/-home-alice-github-example-widget"
	sidA           = ID("3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13")
	sidB           = ID("a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d")
)

func TestReadMeta(t *testing.T) {
	m, err := ReadMeta(filepath.Join(fixtureProject, string(sidA)+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	want := Meta{
		Summary:   "Add verbose flag to widget CLI",
		Title:     "widget-verbose", // custom-title comes after ai-title and wins
		FirstUser: "Add a --verbose flag to the widget CLI",
		LaunchCwd: "/home/alice/github/example/widget",
		WorkCwd:   "/home/alice/github/example/widget/cmd",
		Branch:    "feature/teleport",
		Version:   "2.1.247",
		LastTS:    "2026-08-27T10:16:02.750Z",
	}
	if diff := cmp.Diff(want, m); diff != "" {
		t.Fatal(diff)
	}
	if m.Label() != "widget-verbose" {
		t.Fatalf("Label = %q", m.Label())
	}
}

func TestLabelFallbacks(t *testing.T) {
	if (Meta{}).Label() != "(no summary found)" {
		t.Fatal("empty label")
	}
	if got := (Meta{FirstUser: "  a   b\nc "}).Label(); got != "a b c" {
		t.Fatalf("collapse = %q", got)
	}
	long := Meta{Summary: strings.Repeat("x", 250)}
	if got := long.Label(); len([]rune(got)) != 201 || !strings.HasSuffix(got, "…") {
		t.Fatalf("clip = %q", got)
	}
}

func TestReadMetaSkipsGarbageLines(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.jsonl")
	os.WriteFile(p, []byte("not json\n{\"type\":\"user\",\"cwd\":\"/tmp/a\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"hi\"}]}}\n"), 0o600)
	m, err := ReadMeta(p)
	if err != nil || m.LaunchCwd != "/tmp/a" || m.FirstUser != "hi" {
		t.Fatalf("%+v %v", m, err)
	}
	if _, err := ReadMeta(filepath.Join(t.TempDir(), "missing.jsonl")); err == nil {
		t.Fatal("missing transcript must be an error")
	}
}
