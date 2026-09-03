package sshx

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	skeemaknownhosts "github.com/skeema/knownhosts"
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

// hostKeyAlgorithms returns the host-key algorithm(s) knownHostsFile already
// has recorded for hostWithPort, oldest/first-added entry first (HK-1).
//
// x/crypto/ssh.ClientConfig.HostKeyAlgorithms defaults to a fixed
// preference order (ecdsa before ed25519 among others) applied no matter
// what known_hosts holds; the server then offers whichever algorithm the
// client asked for first, and a host whose known_hosts entry is an older
// key type than that default prefers gets refused as a "mismatch" even
// though the offered AND the known key are both genuinely the host's own
// (real bug: `doctor ten64.example` failed this way while `ssh
// ten64.example` — which orders HostKeyAlgorithms by what's already known —
// worked). OpenSSH avoids this by trying known types first; this mirrors
// that using github.com/skeema/knownhosts, a thin wrapper the project
// already depends on (via go-git) for exactly this lookup.
//
// A missing/unreadable known_hosts file or a host with no entry yet (the
// accept-new first-contact case) returns nil: the caller then leaves
// ClientConfig.HostKeyAlgorithms unset, so x/crypto/ssh's own default order
// applies, unchanged.
func hostKeyAlgorithms(knownHostsFile, hostWithPort string) []string {
	if knownHostsFile == "" {
		return nil
	}
	db, err := skeemaknownhosts.NewDB(knownHostsFile)
	if err != nil {
		return nil
	}
	return db.HostKeyAlgorithms(hostWithPort)
}
