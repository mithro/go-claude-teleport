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

func TestResolveAliasAndProxyJump(t *testing.T) {
	// Test resolving an alias through ssh_config with Host * wildcard
	// The config has Host * defining two IdentityFiles, and Host example.com
	// that redefines one of them. With deduplication, we expect exactly 2 unique entries.
	configPath := "testdata/config"

	tgt, err := ParseTarget("example.com")
	if err != nil {
		t.Fatalf("ParseTarget failed: %v", err)
	}
	tgt.User = "alice"

	resolved, err := Resolve(tgt, configPath)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// Verify HostName resolution
	if resolved.HostName != "example.com" {
		t.Errorf("HostName = %q, want %q", resolved.HostName, "example.com")
	}

	// Verify User resolution
	if resolved.User != "alice" {
		t.Errorf("User = %q, want %q", resolved.User, "alice")
	}

	// Verify Port resolution
	if resolved.Port != 2222 {
		t.Errorf("Port = %d, want %d", resolved.Port, 2222)
	}

	// Verify ProxyJump resolution
	if resolved.ProxyJump != "jump.example" {
		t.Errorf("ProxyJump = %q, want %q", resolved.ProxyJump, "jump.example")
	}

	// Verify deduplication of IdentityFiles
	// Host * defines id_ed25519 and id_rsa
	// Host example.com redefines id_ed25519 (duplicate)
	// After dedup, we expect exactly 2 entries with first occurrence winning
	if len(resolved.IdentityFile) != 2 {
		t.Errorf("IdentityFile count = %d, want 2", len(resolved.IdentityFile))
	}

	expectedIDs := []string{"~/.ssh/id_ed25519", "~/.ssh/id_rsa"}
	if !cmp.Equal(resolved.IdentityFile, expectedIDs) {
		t.Errorf("IdentityFile = %v, want %v", resolved.IdentityFile, expectedIDs)
	}
}
