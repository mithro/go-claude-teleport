package tmuxx

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const describeCmd = `list-panes -a -F "#{session_name}	#{session_group}	#{window_id}	#{window_index}	#{window_name}	#{pane_id}	#{pane_current_path}	#{pane_current_command}	#{pane_pid}	#{history_size}	#{pane_title}"`

func TestDescribe(t *testing.T) {
	f := &Fake{Replies: map[string][]string{
		describeCmd: {
			"main\t\t@0\t0\tshell\t%0\t/home/alice\tbash\t4242\t120\talice@laptop",
			"main\tmain\t@3\t2\tclaude\t%7\t/home/alice/github/x/.worktrees/feat\tclaude\t5150\t9001\t✳ feat\twith tab",
		},
		`show-options -wv -t "@3" automatic-rename`: {"off"},
	}}
	got, err := Describe(context.Background(), f, "%7")
	if err != nil {
		t.Fatal(err)
	}
	want := &Facts{SessionName: "main", Group: "main", WindowID: "@3", WindowIndex: 2, WindowName: "claude", AutoRename: false,
		PaneID: "%7", PaneTitle: "✳ feat\twith tab", PaneCwd: "/home/alice/github/x/.worktrees/feat", PaneCommand: "claude", PanePID: 5150, HistorySize: 9001}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

func TestDescribeAutoRenameOnByDefault(t *testing.T) {
	f := &Fake{Replies: map[string][]string{
		describeCmd: {"main\t\t@0\t0\tshell\t%0\t/home/alice\tbash\t4242\t120\tt"},
		`show-options -wv -t "@0" automatic-rename`: {""},
	}}
	got, err := Describe(context.Background(), f, "%0")
	if err != nil {
		t.Fatal(err)
	}
	if !got.AutoRename {
		t.Error("unset automatic-rename must read as on (tmux default)")
	}
}

func TestDescribeUnknownPane(t *testing.T) {
	f := &Fake{Replies: map[string][]string{describeCmd: {"main\t\t@0\t0\tshell\t%0\t/home/alice\tbash\t4242\t120\tt"}}}
	if _, err := Describe(context.Background(), f, "%99"); err == nil {
		t.Fatal("expected an error for an unknown pane")
	}
}

func TestListSessions(t *testing.T) {
	f := &Fake{Replies: map[string][]string{`list-sessions -F "#{session_name}	#{session_group}"`: {"main\tmain", "main-2\tmain", "other\t"}}}
	got, err := ListSessions(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	want := []SessionInfo{{"main", "main"}, {"main-2", "main"}, {"other", ""}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}
