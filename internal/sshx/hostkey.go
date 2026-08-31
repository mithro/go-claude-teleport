package sshx

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// knownHostsLocks hands out one *sync.Mutex per known_hosts file path, so
// concurrent dials that share a known_hosts file (e.g. parallel hops or
// parallel Dial calls) serialize their check-and-append instead of racing
// the file. The registry itself is guarded by knownHostsLocksMu.
var (
	knownHostsLocksMu sync.Mutex
	knownHostsLocks   = map[string]*sync.Mutex{}
)

func lockForKnownHosts(path string) *sync.Mutex {
	knownHostsLocksMu.Lock()
	defer knownHostsLocksMu.Unlock()
	mu, ok := knownHostsLocks[path]
	if !ok {
		mu = &sync.Mutex{}
		knownHostsLocks[path] = mu
	}
	return mu
}

// hostKeyCallback implements StrictHostKeyChecking yes|accept-new|no over
// one known_hosts file. accept-new appends an UNHASHED line (spec §4.2).
func hostKeyCallback(knownHostsFile, strict string, logf func(string, ...any)) (ssh.HostKeyCallback, error) {
	switch strict {
	case "", "yes", "accept-new", "no":
	default:
		return nil, fmt.Errorf("StrictHostKeyChecking=%q: want yes, accept-new or no", strict)
	}
	if strict == "no" {
		return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			logf("host key checking disabled for %s (%s)", hostname, ssh.FingerprintSHA256(key))
			return nil
		}, nil
	}
	mu := lockForKnownHosts(knownHostsFile)
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		mu.Lock()
		defer mu.Unlock()
		if err := os.MkdirAll(filepath.Dir(knownHostsFile), 0o700); err != nil {
			return fmt.Errorf("known_hosts dir: %w", err)
		}
		f, err := os.OpenFile(knownHostsFile, os.O_RDONLY|os.O_CREATE, 0o600)
		if err != nil {
			return fmt.Errorf("known_hosts %s: %w", knownHostsFile, err)
		}
		f.Close()
		check, err := knownhosts.New(knownHostsFile)
		if err != nil {
			return fmt.Errorf("known_hosts %s: %w", knownHostsFile, err)
		}
		err = check(hostname, remote, key)
		if err == nil {
			return nil
		}
		var ke *knownhosts.KeyError
		if !errors.As(err, &ke) {
			return fmt.Errorf("known_hosts %s: %w", knownHostsFile, err)
		}
		if len(ke.Want) > 0 {
			return fmt.Errorf("host key mismatch for %s: offered %s %s, known_hosts %s has a different key (line %d) — refusing", hostname, key.Type(), ssh.FingerprintSHA256(key), knownHostsFile, ke.Want[0].Line)
		}
		if strict != "accept-new" {
			return fmt.Errorf("unknown host %s: key %s %s is not in %s (re-run with -o StrictHostKeyChecking=accept-new to add it)", hostname, key.Type(), ssh.FingerprintSHA256(key), knownHostsFile)
		}
		line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key) + "\n"
		af, err := os.OpenFile(knownHostsFile, os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return fmt.Errorf("known_hosts %s: %w", knownHostsFile, err)
		}
		defer af.Close()
		if _, err := af.WriteString(line); err != nil {
			return fmt.Errorf("known_hosts %s: %w", knownHostsFile, err)
		}
		logf("added %s (%s) to %s", hostname, ssh.FingerprintSHA256(key), knownHostsFile)
		return nil
	}, nil
}
