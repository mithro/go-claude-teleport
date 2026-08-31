package session

import (
	"bytes"
	"strings"
	"testing"
)

func TestRewriteJSONL(t *testing.T) {
	m := NewPathMap(Mapping{"/home/alice", "/home/bob"})
	in := strings.Join([]string{
		`{"type":"user","cwd":"/home/alice/p","unknownField":{"deep":["/home/alice/q",1756289730123,0.1,1e21,true,null]},"n":12345678901234567890}`,
		`this line is not json`,
		`{"snapshot":{"trackedFileBackups":{"/home/alice/p/main.go":{"version":1}}},"html":"<b>&</b>"}`,
		``,
		`{"nothing":"to rewrite"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	st, err := RewriteJSONL(strings.NewReader(in), &out, m)
	if err != nil {
		t.Fatal(err)
	}
	if st.Records != 4 || st.Rewritten != 2 || st.Unparseable != 1 {
		t.Fatalf("stats %+v", st)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d lines:\n%s", len(lines), out.String())
	}
	checks := []struct{ line int; want []string }{
		{0, []string{`"cwd":"/home/bob/p"`, `"/home/bob/q"`, `1756289730123`, `0.1`, `1e21`, `true`, `null`, `12345678901234567890`, `"unknownField"`}},
		{1, []string{`this line is not json`}},
		{2, []string{`"/home/bob/p/main.go":{"version":1}`, `"<b>&</b>"`}}, // keys rewritten; HTML not escaped
		{4, []string{`{"nothing":"to rewrite"}`}},
	}
	for _, c := range checks {
		for _, w := range c.want {
			if !strings.Contains(lines[c.line], w) {
				t.Errorf("line %d = %s\n  missing %s", c.line, lines[c.line], w)
			}
		}
	}
	if lines[3] != "" {
		t.Errorf("blank line must stay blank: %q", lines[3])
	}
	if lines[1] != "this line is not json" {
		t.Errorf("unparseable line must be verbatim: %q", lines[1])
	}
}

func TestRewriteJSONLLastLineWithoutNewline(t *testing.T) {
	var out bytes.Buffer
	st, err := RewriteJSONL(strings.NewReader(`{"a":"/home/alice"}`), &out, NewPathMap(Mapping{"/home/alice", "/home/bob"}))
	if err != nil || st.Records != 1 || out.String() != "{\"a\":\"/home/bob\"}\n" {
		t.Fatalf("%q %+v %v", out.String(), st, err)
	}
}

func TestRewriteJSON(t *testing.T) {
	in := `{"projects":{"/home/alice/p":{"allowedTools":["Bash(ls /home/alice/p)"]}},"numStartups":12,"keep":"<x>"}`
	var out bytes.Buffer
	st, err := RewriteJSON(strings.NewReader(in), &out, NewPathMap(Mapping{"/home/alice", "/home/bob"}))
	if err != nil || st.Records != 1 || st.Rewritten != 1 {
		t.Fatalf("%+v %v", st, err)
	}
	for _, w := range []string{`"/home/bob/p": {`, `"Bash(ls /home/bob/p)"`, `"numStartups": 12`, `"<x>"`} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("missing %s in\n%s", w, out.String())
		}
	}
	if _, err := RewriteJSON(strings.NewReader("{broken"), &out, NewPathMap()); err == nil {
		t.Fatal("broken document must be an error")
	}
}
