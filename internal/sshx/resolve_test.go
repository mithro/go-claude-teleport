package sshx

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kevinburke/ssh_config"
)

func loadTestConfig(t *testing.T) *ssh_config.Config {
	t.Helper()
	f, err := os.Open("testdata/ssh_config")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, err := ssh_config.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestResolveAliasAndProxyJump(t *testing.T) {
	cfg := loadTestConfig(t)
	r, err := Resolve(Target{Host: "big-storage"}, cfg, nil, "bob")
	if err != nil {
		t.Fatal(err)
	}
	want := Resolved{
		Target: Target{User: "alice", Host: "big-storage", Port: 2222,
			Via: []Target{{Host: "jump"}}},
		HostName:      "big-storage.example",
		IdentityFiles: []string{"~/.ssh/id_storage", "~/.ssh/id_ed25519"},
		Options:       map[string]string{},
	}
	if diff := cmp.Diff(want, r); diff != "" {
		t.Errorf("Resolve mismatch (-want +got):\n%s", diff)
	}
}

func TestResolveDefaultsAndExplicitVia(t *testing.T) {
	cfg := loadTestConfig(t)
	r, err := Resolve(Target{Host: "unknown.example", Via: []Target{{Host: "jump"}}}, cfg, nil, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if r.User != "bob" || r.Port != 22 || r.HostName != "unknown.example" {
		t.Errorf("defaults not applied: %+v", r)
	}
	if diff := cmp.Diff([]Target{{Host: "jump"}}, r.Via); diff != "" {
		t.Errorf("via (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"~/.ssh/id_ed25519"}, r.IdentityFiles); diff != "" {
		t.Errorf("identity files (-want +got):\n%s", diff)
	}
}

func TestResolveConfigProxyJumpPrependedToVia(t *testing.T) {
	cfg := loadTestConfig(t)
	r, err := Resolve(Target{Host: "big-storage", Via: []Target{{Host: "laptop"}}}, cfg, nil, "bob")
	if err != nil {
		t.Fatal(err)
	}
	// config ProxyJump (jump) is the hop closest to the target; --via laptop is outermost.
	want := []Target{{Host: "laptop"}, {Host: "jump"}}
	if diff := cmp.Diff(want, r.Via); diff != "" {
		t.Errorf("via (-want +got):\n%s", diff)
	}
}

func TestResolveOverrides(t *testing.T) {
	cfg := loadTestConfig(t)
	ov := map[string]string{
		"user": "carol", "Port": "2200", "IdentityFile": "/home/alice/.ssh/id_override",
		"StrictHostKeyChecking": "accept-new", "ProxyJump": "a.example,b.example",
	}
	r, err := Resolve(Target{User: "alice", Host: "laptop", Port: 22}, cfg, ov, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if r.User != "carol" || r.Port != 2200 {
		t.Errorf("overrides not applied: %+v", r.Target)
	}
	if diff := cmp.Diff([]string{"/home/alice/.ssh/id_override"}, r.IdentityFiles); diff != "" {
		t.Errorf("identity (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]Target{{Host: "a.example"}, {Host: "b.example"}}, r.Via); diff != "" {
		t.Errorf("via (-want +got):\n%s", diff)
	}
	if r.Options["StrictHostKeyChecking"] != "accept-new" {
		t.Errorf("Options = %v", r.Options)
	}
}

func TestResolveProxyCommandRefused(t *testing.T) {
	cfg := loadTestConfig(t)
	_, err := Resolve(Target{Host: "legacy"}, cfg, nil, "bob")
	if !errors.Is(err, ErrProxyCommand) {
		t.Fatalf("err = %v, want ErrProxyCommand", err)
	}
	if !strings.Contains(err.Error(), "legacy") || !strings.Contains(err.Error(), "--via") {
		t.Errorf("error should name the host and suggest --via: %v", err)
	}
}

func TestResolveNilConfig(t *testing.T) {
	r, err := Resolve(Target{Host: "x.example"}, nil, nil, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if r.HostName != "x.example" || r.User != "bob" || r.Port != 22 {
		t.Errorf("nil config defaults: %+v", r)
	}
}
