package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestTeleportUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{}, // no direction
		{"--to", "a.example", "--from", "b.example"}, // both
		{"--to", "a.example", "--state", "sideways"}, // bad state
		{"--to", "a.example", "--map", "notapair"},   // bad map
	} {
		var out, errb bytes.Buffer
		code := Main(args, strings.NewReader(""), &out, &errb, []string{"HOME=/home/alice", "PATH=/usr/bin"})
		if code != ExitUsage {
			t.Errorf("Main(%v) = %d (%s), want %d", args, code, errb.String(), ExitUsage)
		}
	}
}

func TestParseMaps(t *testing.T) {
	m, err := parseMaps([]string{"/home/alice/a=/srv/a", "/x=/y"})
	if err != nil || len(m) != 2 || m[0].From != "/home/alice/a" || m[1].To != "/y" {
		t.Fatalf("parseMaps = %v %v", m, err)
	}
	if _, err := parseMaps([]string{"relative=/y"}); err == nil {
		t.Error("relative source must be rejected")
	}
}

func TestInternalRunnerUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Main([]string{"internal-runner"}, strings.NewReader(""), &out, &errb, []string{"HOME=/home/alice"}); code != ExitUsage {
		t.Errorf("internal-runner without a job dir = %d", code)
	}
}
