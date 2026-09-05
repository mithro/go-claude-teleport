// Package bootstrap installs the claude-teleport remote helper onto a host
// that does not have it, reconstructing the exact binary for the host's
// architecture from the running (fat) binary's own embedded FATBLOB — no
// download, no matching pre-install. It reuses go-multi-binary's container
// format (fatblob) and arch detection (archdetect); the transport is injected
// so the same code is unit-tested with a fake and driven by sshx in
// production.
package bootstrap

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/mithro/go-multi-binary/archdetect"
	"github.com/mithro/go-multi-binary/fatblob"
)

// ErrNotFatBinary means the running binary carries no FATBLOB — an ordinary
// `go build` (dev) binary. Such a build cannot reconstruct another
// architecture's helper, so bootstrap is impossible and the caller falls back
// to requiring the helper already installed on the remote.
var ErrNotFatBinary = errors.New("this claude-teleport build carries no embedded architectures; cannot bootstrap a remote helper")

// Runner runs one shell command on the remote and returns its stdout. It must
// run cmd through the remote shell (parameter/${...} expansion is used).
type Runner func(ctx context.Context, cmd string) (string, error)

// Putter streams data to remotePath on the remote (e.g. `cat > remotePath`).
type Putter func(ctx context.Context, data []byte, remotePath string) error

// Result summarises a deployment.
type Result struct {
	Arch       string // archdetect id, e.g. "arm64"
	RemotePath string // absolute path of the installed helper on the remote
	MD5        string // md5 of the installed bytes (verified on the remote)
	Size       int
	Reused     bool // the path already held the identical bytes; no upload
}

// IsFat reports whether image is a canonical fat binary (carries a FATBLOB).
func IsFat(image []byte) bool {
	_, _, err := fatblob.SplitCanonical(image)
	return err == nil
}

// Arches lists the architecture ids image carries a real native binary for.
func Arches(image []byte) ([]string, error) {
	_, blob, err := fatblob.SplitCanonical(image)
	if err != nil {
		return nil, ErrNotFatBinary
	}
	out := make([]string, 0, len(blob.Slices))
	for _, s := range blob.Slices {
		if s.Status == fatblob.StatusPresent && len(s.Data) > 0 {
			out = append(out, s.Arch)
		}
	}
	return out, nil
}

// Deploy detects the remote architecture, reconstructs canonical(remoteArch)
// from image (the running binary's own bytes — fatblob.ReadSelf), and installs
// it under the remote's cache directory as
// `<cache>/claude-teleport/claude-teleport-<version>-<arch>`, verifying the
// remote md5 matches the bytes sent. It is idempotent: an existing copy with
// the right md5 is reused without re-uploading. version namespaces the cache
// path so a differently-versioned helper is never silently reused.
func Deploy(ctx context.Context, run Runner, put Putter, image []byte, version string) (Result, error) {
	native, blob, err := fatblob.SplitCanonical(image)
	if err != nil {
		return Result{}, ErrNotFatBinary
	}
	_, _ = native, blob

	uname, err := run(ctx, "uname -m")
	if err != nil {
		return Result{}, fmt.Errorf("detect remote arch: %w", err)
	}
	arch, err := archdetect.FromUname(strings.TrimSpace(uname))
	if err != nil {
		return Result{}, fmt.Errorf("remote arch %q: %w", strings.TrimSpace(uname), err)
	}
	data, err := fatblob.Reconstruct(image, arch)
	if err != nil {
		return Result{}, fmt.Errorf("reconstruct claude-teleport for %s: %w", arch, err)
	}
	sum := md5.Sum(data)
	want := hex.EncodeToString(sum[:])

	// Cache directory on the remote, honouring $XDG_CACHE_HOME and defaulting
	// to ~/.cache — never the shared system /tmp.
	dirOut, err := run(ctx, `printf %s "${XDG_CACHE_HOME:-$HOME/.cache}/claude-teleport"`)
	if err != nil {
		return Result{}, fmt.Errorf("resolve remote cache dir: %w", err)
	}
	dir := strings.TrimSpace(dirOut)
	if dir == "" || dir == "/" || strings.HasPrefix(dir, "/tmp/") {
		return Result{}, fmt.Errorf("refusing to use remote cache dir %q", dir)
	}
	path := dir + "/" + fmt.Sprintf("claude-teleport-%s-%s", version, arch)

	// Idempotent reuse: `test -f` is quiet, and `|| true` swallows only the
	// not-found exit — no stderr is suppressed.
	if out, err := run(ctx, "test -f "+shellQuote(path)+" && md5sum "+shellQuote(path)+" || true"); err == nil && firstField(out) == want {
		return Result{Arch: arch, RemotePath: path, MD5: want, Size: len(data), Reused: true}, nil
	}

	if _, err := run(ctx, "mkdir -p "+shellQuote(dir)); err != nil {
		return Result{}, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp := path + ".incoming"
	if err := put(ctx, data, tmp); err != nil {
		return Result{}, fmt.Errorf("upload: %w", err)
	}
	if _, err := run(ctx, "chmod +x "+shellQuote(tmp)); err != nil {
		return Result{}, fmt.Errorf("chmod: %w", err)
	}
	out, err := run(ctx, "md5sum "+shellQuote(tmp))
	if err != nil {
		return Result{}, fmt.Errorf("verify upload: %w", err)
	}
	if got := firstField(out); got != want {
		// Leave nothing half-installed behind.
		_, _ = run(ctx, "rm -f "+shellQuote(tmp))
		return Result{}, fmt.Errorf("md5 mismatch after upload: sent %s, remote has %s", want, got)
	}
	if _, err := run(ctx, "mv -f "+shellQuote(tmp)+" "+shellQuote(path)); err != nil {
		return Result{}, fmt.Errorf("install: %w", err)
	}
	return Result{Arch: arch, RemotePath: path, MD5: want, Size: len(data)}, nil
}

func firstField(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i]
	}
	return s
}

// shellQuote single-quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
