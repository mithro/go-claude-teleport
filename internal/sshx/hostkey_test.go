package sshx

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/sshx/sshtest"
)

func TestHostKeyStrictYes(t *testing.T) {
	_, known := sshtest.GenKey(t)
	_, other := sshtest.GenKey(t)
	file := filepath.Join(t.TempDir(), "known_hosts")
	os.WriteFile(file, []byte(sshtest.KnownHostsLine("big-storage.example", known)), 0o600)
	addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22}

	cb, err := hostKeyCallback(file, "", t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if err := cb("big-storage.example:22", addr, known); err != nil {
		t.Errorf("known key rejected: %v", err)
	}
	err = cb("unknown.example:22", addr, known)
	if err == nil || !strings.Contains(err.Error(), "SHA256:") {
		t.Errorf("unknown host: err=%v, want fingerprint error", err)
	}
	err = cb("big-storage.example:22", addr, other)
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("changed key: err=%v, want mismatch error", err)
	}
}

func TestHostKeyAcceptNewAppendsUnhashed(t *testing.T) {
	_, key := sshtest.GenKey(t)
	file := filepath.Join(t.TempDir(), "sub", "known_hosts") // dir does not exist yet
	addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 2222}
	cb, err := hostKeyCallback(file, "accept-new", t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if err := cb("big-storage.example:2222", addr, key); err != nil {
		t.Fatalf("accept-new first contact: %v", err)
	}
	data, _ := os.ReadFile(file)
	if !strings.HasPrefix(string(data), "[big-storage.example]:2222 ssh-ed25519 ") {
		t.Errorf("known_hosts line = %q (must be unhashed, bracketed non-22 port)", data)
	}
	// second contact with the same key is fine; a different key is a mismatch
	if err := cb("big-storage.example:2222", addr, key); err != nil {
		t.Errorf("second contact: %v", err)
	}
	_, other := sshtest.GenKey(t)
	if err := cb("big-storage.example:2222", addr, other); err == nil {
		t.Errorf("accept-new must still refuse a changed key")
	}
}

func TestHostKeyStrictNo(t *testing.T) {
	_, key := sshtest.GenKey(t)
	cb, err := hostKeyCallback(filepath.Join(t.TempDir(), "kh"), "no", t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if err := cb("x.example:22", &net.TCPAddr{}, key); err != nil {
		t.Errorf("strict=no rejected: %v", err)
	}
	if _, err := hostKeyCallback("kh", "maybe", t.Logf); err == nil {
		t.Errorf("bad StrictHostKeyChecking value must be an error")
	}
}
