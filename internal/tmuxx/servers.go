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

// FindServer implements spec §9 discovery over socketDir: override (must be
// alive), else preferredName, else "default", else the only live socket,
// else an error listing what was found. It never starts a server.
func FindServer(socketDir string, preferredName string, override string) (string, error) {
	if override != "" {
		p := filepath.Join(socketDir, override)
		if !alive(p) {
			return "", fmt.Errorf("--tmux-socket %s: no live server at %s: %w", override, p, ErrNoServer)
		}
		return p, nil
	}
	for _, name := range []string{preferredName, "default"} {
		if name == "" {
			continue
		}
		if p := filepath.Join(socketDir, name); alive(p) {
			return p, nil
		}
	}
	all, err := ListServers(socketDir)
	if err != nil {
		return "", err
	}
	var live []string
	for _, p := range all {
		if alive(p) {
			live = append(live, p)
		}
	}
	switch len(live) {
	case 0:
		return "", fmt.Errorf("no live tmux server under %s: %w", socketDir, ErrNoServer)
	case 1:
		return live[0], nil
	}
	return "", fmt.Errorf("several tmux servers under %s and none is named %q or \"default\": %s (use --tmux-socket NAME)", socketDir, preferredName, strings.Join(live, ", "))
}
