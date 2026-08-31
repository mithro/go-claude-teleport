package session

import "testing"

func TestArgvSessionID(t *testing.T) {
	const u = "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13"
	cases := []struct {
		argv        []string
		sid         string
		placeholder bool
		ok          bool
	}{
		{[]string{"claude"}, "", false, true},
		{[]string{"/home/alice/.local/bin/claude", "--resume", u}, u, false, true},
		{[]string{"claude", "-r", u}, u, false, true},
		{[]string{"node", "/home/alice/.claude/local/node_modules/@anthropic-ai/claude-code/cli.js", "--resume", u}, u, false, true},
		{[]string{"claude-resume", u}, u, true, true},
		{[]string{"python3", "/home/alice/bin/claude-resume", u, "--saved-output", "/tmp/x"}, u, true, true},
		{[]string{"/usr/bin/go-tmux-saver", "claude-resume", u}, u, true, true},
		{[]string{"claude-teleport", "placeholder", "--resume", u, "--now"}, u, true, true},
		{[]string{"/usr/bin/claude-teleport", "placeholder", "--saved-output", "/tmp/c", "--resume", u}, u, true, true},
		{[]string{"bash"}, "", false, false},
		{[]string{"foo-claude-resume", u}, "", false, false},
		{[]string{"claude-teleport", "list"}, "", false, false},
	}
	for _, c := range cases {
		sid, ph, ok := ArgvSessionID(c.argv)
		if sid != c.sid || ph != c.placeholder || ok != c.ok {
			t.Errorf("ArgvSessionID(%q) = %q %v %v, want %q %v %v", c.argv, sid, ph, ok, c.sid, c.placeholder, c.ok)
		}
	}
}
