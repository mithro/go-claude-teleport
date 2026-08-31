package sshx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/sshx/sshtest"
)

func TestAuthMethodsFromIdentityFiles(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	os.MkdirAll(sshDir, 0o700)
	sshtest.WriteKeyFile(t, sshDir, "id_ed25519", "")
	extra, _ := sshtest.WriteKeyFile(t, home, "id_storage", "")

	methods, cleanup, err := authMethods("", []string{"~/id_storage", "~/.ssh/missing"}, home, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	// one method per key file: id_storage (explicit) + id_ed25519 (default)
	if len(methods) != 2 {
		t.Errorf("got %d methods, want 2 (explicit %s + default id_ed25519)", len(methods), extra)
	}
}

func TestAuthMethodsPassphraseIsClearError(t *testing.T) {
	home := t.TempDir()
	path, _ := sshtest.WriteKeyFile(t, home, "id_locked", "hunter2")
	_, _, err := authMethods("", []string{path}, home, t.Logf)
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("err = %v, want ErrPassphrase", err)
	}
	if !contains(err.Error(), "id_locked") {
		t.Errorf("error should name the file: %v", err)
	}
}

func TestAuthMethodsNoKeysIsError(t *testing.T) {
	_, _, err := authMethods("", nil, t.TempDir(), t.Logf)
	if err == nil {
		t.Fatal("expected an error when no key source is available")
	}
}

func TestExpandHome(t *testing.T) {
	if got := expandHome("~/.ssh/id_x", "/home/alice"); got != "/home/alice/.ssh/id_x" {
		t.Errorf("expandHome = %q", got)
	}
	if got := expandHome("/abs", "/home/alice"); got != "/abs" {
		t.Errorf("expandHome abs = %q", got)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
