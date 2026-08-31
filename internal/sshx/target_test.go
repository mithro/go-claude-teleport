package sshx

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseTarget(t *testing.T) {
	cases := []struct {
		in   string
		want Target
		err  bool
	}{
		{"big-storage.example", Target{Host: "big-storage.example"}, false},
		{"alice@big-storage.example", Target{User: "alice", Host: "big-storage.example"}, false},
		{"alice@big-storage.example:2222", Target{User: "alice", Host: "big-storage.example", Port: 2222}, false},
		{"[fd00::1]:2222", Target{Host: "fd00::1", Port: 2222}, false},
		{"", Target{}, true},
		{"@host", Target{}, true},
		{"host:notaport", Target{}, true},
		{"host:0", Target{}, true},
	}
	for _, c := range cases {
		got, err := ParseTarget(c.in)
		if (err != nil) != c.err {
			t.Errorf("ParseTarget(%q) err=%v want err=%v", c.in, err, c.err)
			continue
		}
		if !c.err && !cmp.Equal(got, c.want) {
			t.Errorf("ParseTarget(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestQuote(t *testing.T) {
	got := Quote([]string{"claude-teleport", "remote", "stream", "tar", "job 1", "it's"})
	want := `claude-teleport remote stream tar 'job 1' 'it'\''s'`
	if got != want {
		t.Errorf("Quote = %s, want %s", got, want)
	}
	if Quote(nil) != "" {
		t.Errorf("Quote(nil) should be empty")
	}
}
