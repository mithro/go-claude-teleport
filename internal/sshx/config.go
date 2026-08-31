package sshx

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	sshconfig "github.com/kevinburke/ssh_config"
)

// Resolved is a target resolved through ssh_config with HostName, User, Port, IdentityFile, and ProxyJump.
type Resolved struct {
	HostName     string
	User         string
	Port         int      // 0 = not specified; ssh default is 22
	IdentityFile []string // deduplicated identity files
	ProxyJump    string   // resolved host from ProxyJump directive; empty if none
}

// Resolve resolves a target through ~/.ssh/config, applying ssh_config rules and deduplicating IdentityFiles.
// It returns the resolved connection details including ProxyJump.
// The configPath can be empty to use the default ~/.ssh/config.
func Resolve(t Target, configPath string) (*Resolved, error) {
	if configPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve: cannot find home dir: %w", err)
		}
		configPath = filepath.Join(home, ".ssh", "config")
	}

	f, err := os.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("resolve: cannot open ssh config %q: %w", configPath, err)
	}
	defer f.Close()

	cfg, err := sshconfig.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("resolve: cannot parse ssh config %q: %w", configPath, err)
	}

	// Use the alias (Host field) to look up config
	host := t.Host

	r := &Resolved{
		HostName: host,
		User:     t.User,
		Port:     t.Port,
	}

	// Get single-value fields from ssh_config
	if hn, err := cfg.Get(host, "HostName"); err == nil && hn != "" {
		r.HostName = hn
	}

	if u, err := cfg.Get(host, "User"); err == nil && u != "" {
		r.User = u
	}

	if p, err := cfg.Get(host, "Port"); err == nil && p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n <= 65535 {
			r.Port = n
		}
	}

	if pj, err := cfg.Get(host, "ProxyJump"); err == nil && pj != "" {
		r.ProxyJump = pj
	}

	// Get all IdentityFile values and deduplicate them
	// ssh_config.GetAll returns all matching values including Host * wildcard
	// Dedupe with a seen map, keeping first occurrence only
	if idFiles, err := cfg.GetAll(host, "IdentityFile"); err == nil && len(idFiles) > 0 {
		seen := make(map[string]bool)
		for _, idFile := range idFiles {
			if idFile != "" && !seen[idFile] {
				r.IdentityFile = append(r.IdentityFile, idFile)
				seen[idFile] = true
			}
		}
	}

	// Apply command-line overrides if specified in Target
	if t.Port != 0 {
		r.Port = t.Port
	}
	if t.User != "" {
		r.User = t.User
	}

	return r, nil
}
