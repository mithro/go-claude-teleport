package session

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestScanUsage(t *testing.T) {
	s, err := Load(fixturePaths(), sidA, nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := ScanUsage(s)
	if err != nil {
		t.Fatal(err)
	}
	want := &Usage{
		MCPServers:      map[string]bool{"playwright": true, "filesystem": true}, // filesystem comes from the sub-agent transcript
		Skills:          map[string]bool{"superpowers:test-driven-development": true},
		Plugins:         map[string]bool{"superpowers@claude-plugins-official": true},
		SubagentTypes:   map[string]bool{"Explore": true},
		PermissionModes: map[string]bool{"acceptEdits": true},
	}
	if diff := cmp.Diff(want, u); diff != "" {
		t.Fatal(diff)
	}
}

func TestScanUsageEmptySession(t *testing.T) {
	s, err := Load(fixturePaths(), sidB, nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := ScanUsage(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(u.MCPServers)+len(u.Skills)+len(u.Plugins)+len(u.SubagentTypes)+len(u.PermissionModes) != 0 {
		t.Fatalf("%+v", u)
	}
}
