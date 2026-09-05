package remote

import (
	"bytes"
	"context"
	"fmt"

	"github.com/mithro/go-claude-teleport/internal/bootstrap"
	"github.com/mithro/go-claude-teleport/internal/sshx"
	"github.com/mithro/go-claude-teleport/internal/version"
	"github.com/mithro/go-multi-binary/fatblob"
)

// BootstrapOptions controls the zero-install remote-helper bootstrap.
type BootstrapOptions struct {
	// Enabled gates bootstrap entirely (a --no-bootstrap escape hatch sets it
	// false). When false the behaviour is exactly the pre-bootstrap one: use
	// the installed helper, or fail if it is absent.
	Enabled bool
	// SelfImage is the running binary's own bytes. nil means read them from
	// /proc/self/exe on demand (fatblob.ReadSelf); tests inject a fat image.
	SelfImage []byte
}

// sshRunner runs a bootstrap probe on the remote, wrapped in `/bin/sh -c` so
// the POSIX syntax it uses (${VAR:-default}, test, ||) is interpreted the same
// way regardless of the account's login shell — the same reason remoteCommand
// does it (HK-2).
func sshRunner(ssh *sshx.Client) bootstrap.Runner {
	return func(ctx context.Context, cmd string) (string, error) {
		out, _, err := ssh.Run(ctx, "/bin/sh -c "+sshx.Quote([]string{cmd}), nil)
		return string(out), err
	}
}

// sshPutter streams data into remotePath via `cat >`, also under /bin/sh -c.
func sshPutter(ssh *sshx.Client) bootstrap.Putter {
	return func(ctx context.Context, data []byte, remotePath string) error {
		cmd := "cat > " + shellQuote(remotePath)
		_, _, err := ssh.Run(ctx, "/bin/sh -c "+sshx.Quote([]string{cmd}), bytes.NewReader(data))
		return err
	}
}

func shellQuote(s string) string { return sshx.Quote([]string{s}) }

// NewClientOrBootstrap connects to the remote helper. If it is absent, or
// present but a different version/protocol than this binary, and bootstrap is
// enabled and this is a fat binary, it reconstructs and installs the matching
// helper for the remote's architecture (no download, no pre-install) and
// connects to that instead. Falls back to the plain installed-helper behaviour
// otherwise.
func NewClientOrBootstrap(ctx context.Context, ssh *sshx.Client, opts BootstrapOptions, logf func(string, ...any)) (*Client, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	connect := func(exe string) (*Client, error) { return NewClient(ctx, ssh, exe, logf) }

	// The common case: the helper is installed and matches. Take it without
	// ever reading our own (large) binary off disk.
	c, err := connect("claude-teleport")
	if err == nil && c.Info().Version == version.Version && c.Info().Protocol == version.Protocol {
		return c, nil
	}

	image := opts.SelfImage
	if image == nil && opts.Enabled {
		if img, rerr := fatblob.ReadSelf(); rerr == nil {
			image = img
		}
	}
	deploy := func() (bootstrap.Result, error) {
		return bootstrap.Deploy(ctx, sshRunner(ssh), sshPutter(ssh), image, version.Version)
	}
	return decideClient(c, err, connect, deploy, image, version.Version, version.Protocol, opts.Enabled, ssh.String(), logf)
}

type connectFunc func(exe string) (*Client, error)

// decideClient holds the installed-vs-bootstrap decision given the result of
// the initial connect (c/connectErr). connect is used only for the second,
// bootstrapped connection; deploy and image are injectable so tests exercise
// this without a real ssh client. ssh is only a label for messages.
func decideClient(c *Client, connectErr error, connect connectFunc, deploy func() (bootstrap.Result, error), image []byte, wantVer string, wantProto int, enabled bool, ssh string, logf func(string, ...any)) (*Client, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if connectErr == nil && c.Info().Version == wantVer && c.Info().Protocol == wantProto {
		return c, nil // installed and matching — the fast path, no upload
	}

	canBootstrap := enabled && bootstrap.IsFat(image)
	if !canBootstrap {
		if connectErr == nil {
			// Installed but mismatched, and we won't replace it: hand it back
			// so the caller's own version check reports the mismatch.
			return c, nil
		}
		return nil, unbootstrappableError(connectErr, enabled, image)
	}

	if connectErr == nil {
		logf("remote %s: installed claude-teleport is %s (want %s); installing a matching helper", ssh, c.Info().Version, wantVer)
		c.Close()
	}
	res, derr := deploy()
	if derr != nil {
		return nil, fmt.Errorf("bootstrap remote helper on %s: %w", ssh, derr)
	}
	verb := "installed"
	if res.Reused {
		verb = "reused"
	}
	logf("remote %s: %s claude-teleport %s (%s) at %s", ssh, verb, wantVer, res.Arch, res.RemotePath)
	return connect(res.RemotePath)
}

// unbootstrappableError explains why an absent remote helper could not be
// bootstrapped, so the failure is actionable rather than a bare "not found".
func unbootstrappableError(orig error, enabled bool, image []byte) error {
	switch {
	case !enabled:
		return fmt.Errorf("%w (bootstrap disabled; install claude-teleport on the remote or drop --no-bootstrap)", orig)
	case !bootstrap.IsFat(image):
		return fmt.Errorf("%w (this claude-teleport build is not a fat binary, so it cannot install a remote helper; install claude-teleport on the remote or use a release build)", orig)
	default:
		return orig
	}
}
