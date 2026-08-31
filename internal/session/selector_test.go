package session

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseSelector(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  Env
		want Selector
		err  string
	}{
		{"inside session", nil, Env{SessionID: "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13", TmuxPane: "%7"},
			Selector{Current: true, ID: sidA, TmuxPane: "%7"}, ""},
		{"current pane", nil, Env{TmuxPane: "%7"}, Selector{Current: true, TmuxPane: "%7"}, ""},
		{"nothing to go on", nil, Env{}, Selector{Current: true}, ""},
		{"bad env session id", nil, Env{SessionID: "garbage"}, Selector{}, "CLAUDE_CODE_SESSION_ID"},
		{"full uuid", []string{"3F9C2B7E-5A14-4D8E-9B21-7C0E5D6A8F13"}, Env{}, Selector{ID: sidA}, ""},
		{"prefix", []string{"3f9c"}, Env{}, Selector{Prefix: "3f9c"}, ""},
		{"name", []string{"widget"}, Env{}, Selector{Prefix: "widget"}, ""},
		{"too short hex", []string{"3f9"}, Env{}, Selector{}, "at least 4"},
		{"tmux window", []string{"main", "3"}, Env{}, Selector{TmuxSess: "main", TmuxWindow: "3"}, ""},
		{"too many", []string{"a", "b", "c"}, Env{}, Selector{}, "too many"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseSelector(c.args, c.env)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want containing %q", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(c.want, got); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}
