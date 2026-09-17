package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh/agent"

	"github.com/mithro/go-claude-teleport/internal/sshx/sshtest"
)

// testAgent serves an in-memory ssh-agent holding one key on a unix
// socket, and returns its path.
func testAgent(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ring := agent.NewKeyring()
	if err := ring.Add(agent.AddedKey{PrivateKey: priv}); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(t.TempDir(), "a.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix socket unavailable: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go agent.ServeAgent(ring, c)
		}
	}()
	return sock
}

// A passphrase-protected identity file is not a reason to give up when an
// ssh-agent is available -- the agent very often holds that exact key,
// which is how OpenSSH itself gets in. authMethods built the agent method
// first and then threw it away on reaching the locked file, so a host
// whose ssh config names an encrypted key could not connect at all.
//
// Real case (2026-09-17): from x1c-work and from ten64, with
// SSH_AUTH_SOCK set and `ssh-add -l` listing the very key named in the
// config, every `claude-teleport doctor` failed with "identity file
// /home/tim/.ssh/keys/new_misc_key: private key is passphrase-protected
// (load it into ssh-agent)".
func TestAuthMethodsKeepsAgentWhenAnIdentityFileIsLocked(t *testing.T) {
	home := t.TempDir()
	locked, _ := sshtest.WriteKeyFile(t, home, "id_locked", "hunter2")
	sock := testAgent(t)

	methods, cleanup, err := authMethods(sock, []string{locked}, home, t.Logf)
	if err != nil {
		t.Fatalf("authMethods = %v, want the agent method despite the locked file", err)
	}
	defer cleanup()
	if len(methods) == 0 {
		t.Fatal("no auth methods: the agent method was discarded")
	}
}

// A usable key file alongside a locked one must still be offered: the
// locked file is skipped, not fatal.
func TestAuthMethodsSkipsLockedFileAndKeepsUsableOne(t *testing.T) {
	home := t.TempDir()
	locked, _ := sshtest.WriteKeyFile(t, home, "id_locked", "hunter2")
	good, _ := sshtest.WriteKeyFile(t, home, "id_good", "")

	methods, cleanup, err := authMethods("", []string{locked, good}, home, t.Logf)
	if err != nil {
		t.Fatalf("authMethods = %v, want the usable key to survive", err)
	}
	defer cleanup()
	if len(methods) != 1 {
		t.Errorf("got %d methods, want 1 (the unlocked key only)", len(methods))
	}
}

// With NO other way in, the passphrase error must still be what the user
// sees -- it names the file and says exactly what to do. Losing that
// message would trade one bad failure for a vaguer one.
func TestAuthMethodsStillReportsPassphraseWhenNothingElseWorks(t *testing.T) {
	home := t.TempDir()
	locked, _ := sshtest.WriteKeyFile(t, home, "id_locked", "hunter2")

	_, _, err := authMethods("", []string{locked}, home, t.Logf)
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("err = %v, want ErrPassphrase", err)
	}
	if !contains(err.Error(), "id_locked") {
		t.Errorf("error should name the file: %v", err)
	}
}
