package sshx

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/kevinburke/ssh_config"
)

// Resolved is a Target with ssh_config and -o overrides applied.
type Resolved struct {
	Target
	HostName      string            // from ssh_config HostName or Host
	IdentityFiles []string          // "~" NOT expanded here; Dial expands with Options.Home
	Options       map[string]string // remaining -o overrides (canonical key case)
}

// ErrProxyCommand is returned when the config uses ProxyCommand for the host.
var ErrProxyCommand = errors.New("ProxyCommand is not supported")

// canonicalOption maps a case-insensitive -o key to its canonical spelling.
func canonicalOption(k string) string {
	switch strings.ToLower(k) {
	case "user":
		return "User"
	case "port":
		return "Port"
	case "identityfile":
		return "IdentityFile"
	case "proxyjump":
		return "ProxyJump"
	case "stricthostkeychecking":
		return "StrictHostKeyChecking"
	case "userknownhostsfile":
		return "UserKnownHostsFile"
	case "connecttimeout":
		return "ConnectTimeout"
	case "serveraliveinterval":
		return "ServerAliveInterval"
	case "serveralivecountmax":
		return "ServerAliveCountMax"
	}
	return k
}

func cfgGet(cfg *ssh_config.Config, alias, key string) string {
	if cfg == nil {
		return ""
	}
	v, err := cfg.Get(alias, key)
	if err != nil {
		return ""
	}
	return v
}

func cfgGetAll(cfg *ssh_config.Config, alias, key string) []string {
	if cfg == nil {
		return nil
	}
	vs, err := cfg.GetAll(alias, key)
	if err != nil {
		return nil
	}
	return vs
}

func parseJumpList(s string) ([]Target, error) {
	var out []Target
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" || part == "none" {
			continue
		}
		t, err := ParseTarget(part)
		if err != nil {
			return nil, fmt.Errorf("ProxyJump %q: %w", s, err)
		}
		out = append(out, t)
	}
	return out, nil
}

// Resolve applies ~/.ssh/config (Host/HostName/User/Port/IdentityFile/ProxyJump)
// and -o overrides. The chain is outermost-first: explicit --via hops (t.Via)
// come first, then the config's ProxyJump hops, which sit closest to the target.
func Resolve(t Target, cfg *ssh_config.Config, overrides map[string]string, localUser string) (Resolved, error) {
	ov := map[string]string{}
	for k, v := range overrides {
		ov[canonicalOption(k)] = v
	}
	r := Resolved{Target: t, Options: map[string]string{}}

	if pc := cfgGet(cfg, t.Host, "ProxyCommand"); pc != "" && pc != "none" {
		return Resolved{}, fmt.Errorf("host %q: %w (config has ProxyCommand %q); use --via <jump> instead", t.Host, ErrProxyCommand, pc)
	}

	r.HostName = cfgGet(cfg, t.Host, "HostName")
	if r.HostName == "" {
		r.HostName = t.Host
	}

	if u, ok := ov["User"]; ok {
		r.User = u
	} else if r.User == "" {
		r.User = cfgGet(cfg, t.Host, "User")
	}
	if r.User == "" {
		r.User = localUser
	}

	if p, ok := ov["Port"]; ok {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return Resolved{}, fmt.Errorf("-o Port=%q: not a port", p)
		}
		r.Port = n
	} else if r.Port == 0 {
		if p := cfgGet(cfg, t.Host, "Port"); p != "" {
			n, err := strconv.Atoi(p)
			if err != nil {
				return Resolved{}, fmt.Errorf("ssh_config Port %q for %q: %w", p, t.Host, err)
			}
			r.Port = n
		}
	}
	if r.Port == 0 {
		r.Port = 22
	}

	if f, ok := ov["IdentityFile"]; ok {
		r.IdentityFiles = []string{f}
	} else {
		seen := map[string]bool{}
		for _, f := range cfgGetAll(cfg, t.Host, "IdentityFile") {
			if f != "" && !seen[f] {
				r.IdentityFiles = append(r.IdentityFiles, f)
				seen[f] = true
			}
		}
	}

	// OpenSSH reads the keepalive keywords from the config too; an
	// explicit -o wins (the copy loop below writes it over this).
	for _, k := range []string{"ServerAliveInterval", "ServerAliveCountMax"} {
		if _, ok := ov[k]; ok {
			continue
		}
		if v := cfgGet(cfg, t.Host, k); v != "" {
			r.Options[k] = v
		}
	}

	jumpSpec, has := ov["ProxyJump"]
	if !has {
		jumpSpec = cfgGet(cfg, t.Host, "ProxyJump")
	}
	jumps, err := parseJumpList(jumpSpec)
	if err != nil {
		return Resolved{}, fmt.Errorf("host %q: %w", t.Host, err)
	}
	r.Via = append(append([]Target{}, t.Via...), jumps...)

	for k, v := range ov {
		switch k {
		case "User", "Port", "IdentityFile", "ProxyJump":
		default:
			r.Options[k] = v
		}
	}
	return r, nil
}
