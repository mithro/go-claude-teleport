// Package sshtest is an in-process ssh server for tests: exec requests are
// handed to a Go function and direct-tcpip channels are resolved through an
// injected name map, so a "jump" server can forward to a "dest" server whose
// name only the jump knows.
package sshtest

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

// GenKey makes a fresh ed25519 key pair.
func GenKey(t testing.TB) (ssh.Signer, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer, signer.PublicKey()
}

// GenECDSAKey makes a fresh P-256 ECDSA key pair (ssh-type
// ecdsa-sha2-nistp256) — used alongside GenKey to give a test server two
// host keys of different types (HK-1: known_hosts host-key-algorithm
// preference needs at least two types to distinguish).
func GenECDSAKey(t testing.TB) (ssh.Signer, ssh.PublicKey) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer, signer.PublicKey()
}

// WriteKeyFile writes a new OpenSSH-format private key (optionally
// passphrase-protected) to dir/name with mode 0600 and returns its signer.
func WriteKeyFile(t testing.TB, dir, name, passphrase string) (string, ssh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, "sshtest")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "sshtest", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return path, signer
}

// KnownHostsLine renders one unhashed known_hosts line for addr
// ("host" or "[host]:port").
func KnownHostsLine(addr string, key ssh.PublicKey) string {
	return addr + " " + key.Type() + " " + base64Marshal(key) + "\n"
}

func base64Marshal(key ssh.PublicKey) string {
	return base64.StdEncoding.EncodeToString(key.Marshal())
}
