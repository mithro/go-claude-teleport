package sshx

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// ErrPassphrase is returned for an encrypted private key: the tool never
// prompts, so the user must load the key into ssh-agent instead.
var ErrPassphrase = errors.New("private key is passphrase-protected (load it into ssh-agent)")

var defaultIdentityFiles = []string{"~/.ssh/id_ed25519", "~/.ssh/id_rsa", "~/.ssh/id_ecdsa"}

func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// authMethods builds the auth list: ssh-agent (if agentSocket is set and
// connects), then one PublicKeys method per readable identity file.
func authMethods(agentSocket string, identityFiles []string, home string, logf func(string, ...any)) ([]ssh.AuthMethod, func(), error) {
	var methods []ssh.AuthMethod
	cleanup := func() {}
	if agentSocket != "" {
		conn, err := net.Dial("unix", agentSocket)
		if err != nil {
			logf("ssh-agent %s: %v (continuing with key files)", agentSocket, err)
		} else {
			ag := agent.NewClient(conn)
			methods = append(methods, ssh.PublicKeysCallback(ag.Signers))
			cleanup = func() { conn.Close() }
		}
	}
	seen := map[string]bool{}
	for _, f := range append(append([]string{}, identityFiles...), defaultIdentityFiles...) {
		path := expandHome(f, home)
		if seen[path] {
			continue
		}
		seen[path] = true
		raw, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("identity file %s: %w", path, err)
		}
		signer, err := ssh.ParsePrivateKey(raw)
		if err != nil {
			var pm *ssh.PassphraseMissingError
			if errors.As(err, &pm) {
				cleanup()
				return nil, nil, fmt.Errorf("identity file %s: %w", path, ErrPassphrase)
			}
			cleanup()
			return nil, nil, fmt.Errorf("identity file %s: %w", path, err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if len(methods) == 0 {
		return nil, nil, fmt.Errorf("no ssh authentication available: no ssh-agent (SSH_AUTH_SOCK) and no key file among %s", strings.Join(defaultIdentityFiles, ", "))
	}
	return methods, cleanup, nil
}
