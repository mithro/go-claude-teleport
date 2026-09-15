package tmuxx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Dial is the liveness probe FindServer uses; tests replace it.
var Dial Dialer = DialControl

// ListServers returns the socket paths under socketDir (sorted). A missing
// directory means no servers.
func ListServers(socketDir string) ([]string, error) {
	entries, err := os.ReadDir(socketDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSocket != 0 {
			out = append(out, filepath.Join(socketDir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// alive reports whether a control-mode handshake succeeds on path.
func alive(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t, err := Dial(ctx, path)
	if err != nil {
		return false
	}
	t.Close()
	return true
}

// sessionHolders returns the live sockets among paths whose server has a
// session matching target, by the same rule BaseSession uses (an exact
// name, or a member of that group). A server that will not answer is
// skipped rather than fatal: one broken server must not veto discovery.
func sessionHolders(paths []string, target string) []string {
	var out []string
	for _, p := range paths {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		t, err := Dial(ctx, p)
		if err != nil {
			cancel()
			continue
		}
		sessions, err := ListSessions(ctx, t)
		t.Close()
		cancel()
		if err != nil {
			continue
		}
		if _, ok := BaseSession(sessions, target); ok {
			out = append(out, p)
		}
	}
	return out
}

// FindServer implements spec §9 discovery over socketDir: override (must be
// alive), else preferredName, else "default", else the only live socket,
// else an error listing what was found. It never starts a server.
func FindServer(socketDir string, preferredName string, override string) (string, error) {
	return FindServerForSession(socketDir, preferredName, override, "")
}

// FindServerForSession is FindServer with one extra tie-breaker: when
// several servers are live and exactly one of them already holds a session
// matching target, that server wins over the name heuristics.
//
// Which server a session sits on is not recorded anywhere — the registry
// stores "<session>:@win.%pane" with no socket — so before this the
// destination chose by name alone. That was wrong in two ways on a host
// running more than one server: with no name match it refused and demanded
// --tmux-socket even though the answer was discoverable, and with a name
// match it could open a SECOND session of the target's name on the
// preferred server while the real one sat on another.
//
// The tie-breaker is deliberately narrow. It never beats an explicit
// --tmux-socket, which is the user naming the server outright. Two holders
// are no basis for choosing, so that falls back to the established order
// rather than erroring — this may only turn a refusal into a success or
// pick a better server, never break a case that works today. And a single
// live server, the overwhelmingly common case, is returned without a
// session scan at all.
func FindServerForSession(socketDir, preferredName, override, target string) (string, error) {
	if override != "" {
		p := filepath.Join(socketDir, override)
		if !alive(p) {
			return "", fmt.Errorf("--tmux-socket %s: no live server at %s: %w", override, p, ErrNoServer)
		}
		return p, nil
	}
	if target != "" {
		live, err := ListLiveServers(socketDir)
		if err != nil {
			return "", err
		}
		// One server answers everything; scanning it would only cost a
		// round trip to reach the same place the fallback below does.
		if len(live) > 1 {
			if holders := sessionHolders(live, target); len(holders) == 1 {
				return holders[0], nil
			}
		}
	}
	for _, name := range []string{preferredName, "default"} {
		if name == "" {
			continue
		}
		if p := filepath.Join(socketDir, name); alive(p) {
			return p, nil
		}
	}
	live, err := ListLiveServers(socketDir)
	if err != nil {
		return "", err
	}
	switch len(live) {
	case 0:
		return "", fmt.Errorf("no live tmux server under %s: %w", socketDir, ErrNoServer)
	case 1:
		return live[0], nil
	}
	return "", fmt.Errorf("several tmux servers under %s and none is named %q or \"default\": %s (use --tmux-socket NAME)", socketDir, preferredName, strings.Join(live, ", "))
}
