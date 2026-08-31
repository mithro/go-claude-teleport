package session

import (
	"strings"
	"testing"
)

func TestNewPathMapOrdersLongestFirst(t *testing.T) {
	m := NewPathMap(Mapping{"/home/alice", "/home/bob"}, Mapping{"/home/alice/github/example/widget", "/srv/widget"})
	if m[0].From != "/home/alice/github/example/widget" {
		t.Fatalf("order: %+v", m)
	}
	if m.Empty() || !NewPathMap().Empty() {
		t.Fatal("Empty")
	}
}

func TestNewPathMapPanicsOnBadInput(t *testing.T) {
	for _, bad := range [][]Mapping{
		{{"relative", "/x"}},
		{{"/x", "relative"}},
		{{"/x", "/y"}, {"/x", "/z"}},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("no panic for %+v", bad)
				}
			}()
			NewPathMap(bad...)
		}()
	}
}

func TestParseMappings(t *testing.T) {
	ms, err := ParseMappings([]string{"/home/alice=/home/bob", "/a/b/=/c/d/"})
	if err != nil || len(ms) != 2 || ms[0].To != "/home/bob" || ms[1].From != "/a/b" || ms[1].To != "/c/d" {
		t.Fatalf("%+v %v", ms, err)
	}
	for _, bad := range []string{"nope", "=/x", "/x=", "rel=/x", "/x=rel"} {
		if _, err := ParseMappings([]string{bad}); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	_, err = ParseMappings([]string{"/a=/b", "/a=/c"})
	if err == nil || !strings.Contains(err.Error(), "/a") {
		t.Errorf("duplicate From not rejected: %v", err)
	}
}

func TestApplyPath(t *testing.T) {
	m := NewPathMap(Mapping{"/home/alice", "/home/bob"}, Mapping{"/home/alice/github/example/widget", "/srv/widget"})
	cases := map[string]string{
		"/home/alice":                              "/home/bob",
		"/home/alice/x":                            "/home/bob/x",
		"/home/alice/github/example/widget/main.go": "/srv/widget/main.go",
		"/home/alicent/x":                          "/home/alicent/x", // not a boundary
		"/opt/home/alice":                          "/opt/home/alice", // not a prefix
		"relative/home/alice":                      "relative/home/alice",
	}
	for in, want := range cases {
		if got := m.ApplyPath(in); got != want {
			t.Errorf("ApplyPath(%q) = %q want %q", in, got, want)
		}
	}
}

func TestApplyInsideStrings(t *testing.T) {
	m := NewPathMap(Mapping{"/home/alice", "/home/bob"})
	cases := map[string]string{
		"/home/alice/x":                          "/home/bob/x",
		"gitdir: /home/alice/r/.git/worktrees/w": "gitdir: /home/bob/r/.git/worktrees/w",
		"cd /home/alice && ls /home/alice/x":     "cd /home/bob && ls /home/bob/x",
		"see /home/alice.":                       "see /home/bob.",
		"/home/alicent":                          "/home/alicent",
		"x/home/alice":                           "x/home/alice",
		"no paths here":                          "no paths here",
		"\"/home/alice\"":                        "\"/home/bob\"",
	}
	for in, want := range cases {
		if got := m.Apply(in); got != want {
			t.Errorf("Apply(%q) = %q want %q", in, got, want)
		}
	}
	if strings.Contains(NewPathMap().Apply("/home/alice"), "bob") {
		t.Fatal("empty map must be identity")
	}
}
