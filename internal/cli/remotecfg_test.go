package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/sshx/sshtest"
	"github.com/mithro/go-claude-teleport/test/fakeclaude/harness"
)

// remoteHost starts an sshtest server whose exec handler runs THIS cli's
// `remote serve` in-process with remoteEnv, and returns "host:port" + the
// -o flags the client needs (key file, accept-new).
func remoteHost(t *testing.T, remoteEnv []string) (string, []string, string) {
	t.Helper()
	return remoteHostExec(t, func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
		f := strings.Fields(cmd)
		if len(f) < 3 || f[1] != "remote" {
			io.WriteString(stderr, "unexpected: "+cmd)
			return 127
		}
		return Main(f[1:], stdin, stdout, stderr, remoteEnv)
	})
}

// remoteHostExec is remoteHost with the far side's exec handler supplied
// by the caller — for a peer that must answer differently from this
// binary (a mismatched version, say).
func remoteHostExec(t *testing.T, exec func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int) (string, []string, string) {
	t.Helper()
	localHome := filepath.Join(t.TempDir(), "home", "alice")
	os.MkdirAll(filepath.Join(localHome, ".ssh"), 0o700)
	keyPath, signer := sshtest.WriteKeyFile(t, filepath.Join(localHome, ".ssh"), "id_ed25519", "")
	srv := sshtest.New(t, sshtest.Options{
		Authorized: []ssh.PublicKey{signer.PublicKey()},
		Exec:       exec,
	})
	host, port, _ := net.SplitHostPort(srv.Addr)
	target := "bob@" + host + ":" + port
	opts := []string{"-o", "IdentityFile=" + keyPath, "-o", "StrictHostKeyChecking=accept-new"}
	return target, opts, localHome
}

func writeSettings(t *testing.T, cfgDir, hooks string) {
	t.Helper()
	os.MkdirAll(cfgDir, 0o700)
	os.WriteFile(filepath.Join(cfgDir, "settings.json"), []byte(`{"hooks":`+hooks+`,"permissions":{"defaultMode":"default"}}`), 0o600)
}

func TestCompareConfigRemote(t *testing.T) {
	remoteEnv, remoteHome := testEnv(t)
	writeSettings(t, filepath.Join(remoteHome, ".claude"), `{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"echo remote"}]}]}`)
	target, opts, localHome := remoteHost(t, remoteEnv)
	writeSettings(t, filepath.Join(localHome, ".claude"), `{}`)
	localEnv := []string{"HOME=" + localHome, "USER=alice", "PWD=" + localHome, "PATH=/usr/bin:/bin"}

	var out, errOut bytes.Buffer
	args := append([]string{"compare-config", target}, opts...)
	code := Main(args, strings.NewReader(""), &out, &errOut, localEnv)
	if code != ExitRefused {
		t.Fatalf("hook drift must block: exit %d: out %s err %s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "hooks") || !strings.Contains(out.String(), "block") {
		t.Errorf("hook drift must be reported as block:\n%s", out.String())
	}
	kh, _ := os.ReadFile(filepath.Join(localHome, ".ssh", "known_hosts"))
	if !strings.Contains(string(kh), "ssh-ed25519") {
		t.Errorf("accept-new should have recorded the host key")
	}

	out.Reset()
	errOut.Reset()
	code = Main(append(append([]string{"compare-config", target}, opts...), "--json"), strings.NewReader(""), &out, &errOut, localEnv)
	if code != ExitRefused || !strings.HasPrefix(strings.TrimSpace(out.String()), "{") {
		t.Errorf("--json: exit %d out %q err %q", code, out.String(), errOut.String())
	}

	out.Reset()
	errOut.Reset()
	code = Main(append(append([]string{"compare-config", target}, opts...), "--allow-config-drift"), strings.NewReader(""), &out, &errOut, localEnv)
	if code != ExitOK {
		t.Errorf("--allow-config-drift must downgrade blocking drift: exit %d err %q", code, errOut.String())
	}
}

// TestCompareConfigRemoteDestCwd covers --dest-cwd: without it, the remote
// InventoryHost call reuses the local cwd, and since the "same" project
// lives under a different path on the remote, its project-scoped mcpServers
// entry is invisible there — spurious "MCP server absent on destination"
// drift, purely from the path mismatch. --dest-cwd points the remote
// inventory at the project's actual remote path and the drift disappears.
func TestCompareConfigRemoteDestCwd(t *testing.T) {
	remoteEnv, remoteHome := testEnv(t)
	remoteCwd := "/home/bob/otherproj"
	mcp := `{"projects":{"` + remoteCwd + `":{"mcpServers":{"widget":{"type":"stdio","command":"widget-mcp"}}}}}`
	if err := os.WriteFile(filepath.Join(remoteHome, ".claude.json"), []byte(mcp), 0o600); err != nil {
		t.Fatal(err)
	}
	target, opts, localHome := remoteHost(t, remoteEnv)
	localCwd := "/home/alice/work"
	if err := os.WriteFile(filepath.Join(localHome, ".claude.json"), []byte(
		`{"projects":{"`+localCwd+`":{"mcpServers":{"widget":{"type":"stdio","command":"widget-mcp"}}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	localEnv := []string{"HOME=" + localHome, "USER=alice", "PWD=" + localCwd, "PATH=/usr/bin:/bin"}

	var out, errOut bytes.Buffer
	args := append([]string{"compare-config", target}, opts...)
	code := Main(args, strings.NewReader(""), &out, &errOut, localEnv)
	if code != ExitRefused {
		t.Fatalf("without --dest-cwd, mismatched project paths must spuriously block: exit %d out %s err %s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "mcp.widget") {
		t.Errorf("expected spurious mcp.widget drift:\n%s", out.String())
	}

	out.Reset()
	errOut.Reset()
	args = append([]string{"compare-config", target, "--dest-cwd", remoteCwd}, opts...)
	code = Main(args, strings.NewReader(""), &out, &errOut, localEnv)
	if code != ExitOK {
		t.Fatalf("--dest-cwd should point at the matching remote project and clear the drift: exit %d out %s err %s", code, out.String(), errOut.String())
	}
	if strings.Contains(out.String(), "mcp.widget") {
		t.Errorf("--dest-cwd should have removed the mcp.widget drift:\n%s", out.String())
	}
}

func TestCompareConfigUnreachable(t *testing.T) {
	_, localHome := testEnv(t)
	localEnv := []string{"HOME=" + localHome, "USER=alice", "PATH=/bin"}
	var out, errOut bytes.Buffer
	code := Main([]string{"compare-config", "nobody@127.0.0.1:1", "-o", "StrictHostKeyChecking=no"}, strings.NewReader(""), &out, &errOut, localEnv)
	if code != ExitUnreachable {
		t.Errorf("exit %d, want %d: %s", code, ExitUnreachable, errOut.String())
	}
}

// TestInspectHostUnknownSessionIsRefused covers newInspectCmd's --host: the
// session is always resolved LOCALLY first (spec: inspect shows what a
// teleport of the LOCAL session would move; --host only adds the preflight
// plan/drift against a candidate destination — it does not inspect a
// session that merely happens to live on some other host, which was Plan
// 02's inspectRemote behaviour and is superseded here). A session absent
// locally must fail before --host is ever consulted.
func TestInspectHostUnknownSessionIsRefused(t *testing.T) {
	remoteEnv, _ := testEnv(t)
	target, opts, localHome := remoteHost(t, remoteEnv)
	localEnv := []string{"HOME=" + localHome, "USER=alice", "PATH=/bin"}
	var out, errOut bytes.Buffer
	code := Main(append([]string{"inspect", tsid, "--host", target}, opts...), strings.NewReader(""), &out, &errOut, localEnv)
	if code == ExitOK || !strings.Contains(errOut.String(), "not") {
		t.Errorf("exit %d stderr %q", code, errOut.String())
	}
}

// TestInspectHostShowsPlan drives inspect --host against a real (loopback)
// remote over the sshtest harness: a local session resolves, preflight
// runs against the destination exactly as a teleport would, and the
// rendered plan appears in the output.
func TestInspectHostShowsPlan(t *testing.T) {
	claudeDir := harness.Build(t)
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", claudeDir+string(os.PathListSeparator)+oldPath)
	noTmux := filepath.Join(t.TempDir(), "no-tmux-here")

	remoteEnv, remoteHome := testEnv(t)
	remoteEnv = append(remoteEnv, "TMUX_TMPDIR="+noTmux)
	os.MkdirAll(filepath.Join(remoteHome, ".claude"), 0o700)
	target, opts, localHome := remoteHost(t, remoteEnv)
	localEnv := []string{"HOME=" + localHome, "USER=alice", "PATH=" + os.Getenv("PATH"), "TMUX_TMPDIR=" + noTmux}

	cwd := filepath.Join(localHome, "proj")
	os.MkdirAll(cwd, 0o755)
	seed := exec.Command("claude", "-p", "--session-id", tsid, "hi")
	seed.Dir = cwd
	seed.Env = append(os.Environ(), "HOME="+localHome, "CLAUDE_CONFIG_DIR="+filepath.Join(localHome, ".claude"), "PATH="+os.Getenv("PATH"))
	if out, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}

	var out, errOut bytes.Buffer
	code := Main(append([]string{"inspect", tsid, "--host", target}, opts...), strings.NewReader(""), &out, &errOut, localEnv)
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out.String(), errOut.String())
	}
	for _, want := range []string{"Plan against " + target, "Files", tsid[:8]} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("inspect --host output missing %q:\n%s", want, out.String())
		}
	}
}

// TestInspectHostDoesNotClobberExistingJobManifest is ruling R-P3-23b:
// orchestrate.Preflight writes jobs/<jobID>/manifest.json on BOTH hosts, so
// running it under the session's REAL id (an interrupted job might already
// have one there, which `continue`'s git-attach and abandon depend on)
// would clobber it. inspect --host must use a throwaway jobID instead —
// seed a manifest.json under the real session id on both hosts first and
// assert both are byte-identical before and after.
func TestInspectHostDoesNotClobberExistingJobManifest(t *testing.T) {
	claudeDir := harness.Build(t)
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", claudeDir+string(os.PathListSeparator)+oldPath)
	noTmux := filepath.Join(t.TempDir(), "no-tmux-here")

	remoteEnv, remoteHome := testEnv(t)
	remoteEnv = append(remoteEnv, "TMUX_TMPDIR="+noTmux)
	os.MkdirAll(filepath.Join(remoteHome, ".claude"), 0o700)
	target, opts, localHome := remoteHost(t, remoteEnv)
	localEnv := []string{"HOME=" + localHome, "USER=alice", "PATH=" + os.Getenv("PATH"), "TMUX_TMPDIR=" + noTmux}

	cwd := filepath.Join(localHome, "proj")
	os.MkdirAll(cwd, 0o755)
	seed := exec.Command("claude", "-p", "--session-id", tsid, "hi")
	seed.Dir = cwd
	seed.Env = append(os.Environ(), "HOME="+localHome, "CLAUDE_CONFIG_DIR="+filepath.Join(localHome, ".claude"), "PATH="+os.Getenv("PATH"))
	if out, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}

	// An existing, interrupted job under the SAME session id, with its own
	// manifest.json, on both hosts.
	localManifest := filepath.Join(localHome, ".local", "share", "claude-teleport", "jobs", tsid, "manifest.json")
	remoteManifest := filepath.Join(remoteHome, ".local", "share", "claude-teleport", "jobs", tsid, "manifest.json")
	os.MkdirAll(filepath.Dir(localManifest), 0o700)
	os.MkdirAll(filepath.Dir(remoteManifest), 0o700)
	localMarker := []byte(`{"marker":"local-interrupted-job","version":1}`)
	remoteMarker := []byte(`{"marker":"remote-interrupted-job","version":1}`)
	if err := os.WriteFile(localManifest, localMarker, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(remoteManifest, remoteMarker, 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	code := Main(append([]string{"inspect", tsid, "--host", target}, opts...), strings.NewReader(""), &out, &errOut, localEnv)
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out.String(), errOut.String())
	}

	gotLocal, err := os.ReadFile(localManifest)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotLocal) != string(localMarker) {
		t.Errorf("local jobs/%s/manifest.json was clobbered: got %s, want %s", tsid, gotLocal, localMarker)
	}
	gotRemote, err := os.ReadFile(remoteManifest)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotRemote) != string(remoteMarker) {
		t.Errorf("remote jobs/%s/manifest.json was clobbered: got %s, want %s", tsid, gotRemote, remoteMarker)
	}
}

// TestInspectHostNeverLeaksProjectEntrySecrets is ruling R-P3-23d: `inspect
// --host` runs a real preflight, which populates Plan.Extras.ProjectEntry
// from the LOCAL ~/.claude.json project entry verbatim (mcpServers env,
// auth headers, ...) — inspect must never surface it, text or --json.
func TestInspectHostNeverLeaksProjectEntrySecrets(t *testing.T) {
	claudeDir := harness.Build(t)
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", claudeDir+string(os.PathListSeparator)+oldPath)
	noTmux := filepath.Join(t.TempDir(), "no-tmux-here")

	remoteEnv, remoteHome := testEnv(t)
	remoteEnv = append(remoteEnv, "TMUX_TMPDIR="+noTmux)
	os.MkdirAll(filepath.Join(remoteHome, ".claude"), 0o700)
	target, opts, localHome := remoteHost(t, remoteEnv)
	localEnv := []string{"HOME=" + localHome, "USER=alice", "PATH=" + os.Getenv("PATH"), "TMUX_TMPDIR=" + noTmux}

	cwd := filepath.Join(localHome, "proj")
	os.MkdirAll(cwd, 0o755)
	seed := exec.Command("claude", "-p", "--session-id", tsid, "hi")
	seed.Dir = cwd
	seed.Env = append(os.Environ(), "HOME="+localHome, "CLAUDE_CONFIG_DIR="+filepath.Join(localHome, ".claude"), "PATH="+os.Getenv("PATH"))
	if out, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}

	const secret = "hunter2-example"
	globalJSON := filepath.Join(localHome, ".claude.json")
	doc := map[string]any{"projects": map[string]any{cwd: map[string]any{
		"mcpServers": map[string]any{"myserver": map[string]any{
			"command": "npx", "args": []any{"myserver"},
			"env": map[string]any{"SECRET_TOKEN": secret},
		}},
	}}}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalJSON, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	code := Main(append([]string{"inspect", tsid, "--host", target}, opts...), strings.NewReader(""), &out, &errOut, localEnv)
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out.String(), errOut.String())
	}
	if strings.Contains(out.String(), secret) || strings.Contains(out.String(), "SECRET_TOKEN") {
		t.Errorf("inspect --host text output leaked the mcpServers secret:\n%s", out.String())
	}

	out.Reset()
	errOut.Reset()
	code = Main(append([]string{"inspect", tsid, "--host", target, "--json"}, opts...), strings.NewReader(""), &out, &errOut, localEnv)
	if code != ExitOK {
		t.Fatalf("--json exit %d\nstdout: %s\nstderr: %s", code, out.String(), errOut.String())
	}
	if strings.Contains(out.String(), secret) || strings.Contains(out.String(), "SECRET_TOKEN") {
		t.Errorf("inspect --host --json leaked the mcpServers secret:\n%s", out.String())
	}
}

// TestInspectHostRefusedOnBlockingDrift pins folded minor M6: a genuine
// orchestrate.RefusedError from Preflight (here, blocking hook drift) must
// surface through inspect --host as exit 3, with the reason explained.
func TestInspectHostRefusedOnBlockingDrift(t *testing.T) {
	claudeDir := harness.Build(t)
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", claudeDir+string(os.PathListSeparator)+oldPath)
	noTmux := filepath.Join(t.TempDir(), "no-tmux-here")

	remoteEnv, remoteHome := testEnv(t)
	remoteEnv = append(remoteEnv, "TMUX_TMPDIR="+noTmux)
	os.MkdirAll(filepath.Join(remoteHome, ".claude"), 0o700)
	target, opts, localHome := remoteHost(t, remoteEnv)
	localEnv := []string{"HOME=" + localHome, "USER=alice", "PATH=" + os.Getenv("PATH"), "TMUX_TMPDIR=" + noTmux}

	cwd := filepath.Join(localHome, "proj")
	os.MkdirAll(cwd, 0o755)
	writeSettings(t, filepath.Join(localHome, ".claude"), `{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"echo local"}]}]}`)
	seed := exec.Command("claude", "-p", "--session-id", tsid, "hi")
	seed.Dir = cwd
	seed.Env = append(os.Environ(), "HOME="+localHome, "CLAUDE_CONFIG_DIR="+filepath.Join(localHome, ".claude"), "PATH="+os.Getenv("PATH"))
	if out, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}

	var out, errOut bytes.Buffer
	code := Main(append([]string{"inspect", tsid, "--host", target}, opts...), strings.NewReader(""), &out, &errOut, localEnv)
	if code != ExitRefused {
		t.Fatalf("exit %d, want ExitRefused\nstdout: %s\nstderr: %s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "would be refused") {
		t.Errorf("output must explain the refusal:\n%s", out.String())
	}
}

// TestInspectHostLeavesNoThrowawayJobDirBehind is ruling R-P3-23i's
// end-to-end check: after a successful inspect --host, no jobs/inspect-*
// directory remains on either host (the throwaway job's manifest.json,
// via the new remove-job op on the destination and a direct os.RemoveAll
// locally).
func TestInspectHostLeavesNoThrowawayJobDirBehind(t *testing.T) {
	claudeDir := harness.Build(t)
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", claudeDir+string(os.PathListSeparator)+oldPath)
	noTmux := filepath.Join(t.TempDir(), "no-tmux-here")

	remoteEnv, remoteHome := testEnv(t)
	remoteEnv = append(remoteEnv, "TMUX_TMPDIR="+noTmux)
	os.MkdirAll(filepath.Join(remoteHome, ".claude"), 0o700)
	target, opts, localHome := remoteHost(t, remoteEnv)
	localEnv := []string{"HOME=" + localHome, "USER=alice", "PATH=" + os.Getenv("PATH"), "TMUX_TMPDIR=" + noTmux}

	cwd := filepath.Join(localHome, "proj")
	os.MkdirAll(cwd, 0o755)
	seed := exec.Command("claude", "-p", "--session-id", tsid, "hi")
	seed.Dir = cwd
	seed.Env = append(os.Environ(), "HOME="+localHome, "CLAUDE_CONFIG_DIR="+filepath.Join(localHome, ".claude"), "PATH="+os.Getenv("PATH"))
	if out, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}

	var out, errOut bytes.Buffer
	code := Main(append([]string{"inspect", tsid, "--host", target}, opts...), strings.NewReader(""), &out, &errOut, localEnv)
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out.String(), errOut.String())
	}

	localLeftovers, err := filepath.Glob(filepath.Join(localHome, ".local", "share", "claude-teleport", "jobs", "inspect-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(localLeftovers) != 0 {
		t.Errorf("local throwaway job dir(s) left behind: %v", localLeftovers)
	}
	remoteLeftovers, err := filepath.Glob(filepath.Join(remoteHome, ".local", "share", "claude-teleport", "jobs", "inspect-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(remoteLeftovers) != 0 {
		t.Errorf("remote throwaway job dir(s) left behind: %v", remoteLeftovers)
	}
}

// otherVersionEndpoint is a peer running a DIFFERENT claude-teleport
// release: everything else answers normally, Hello reports another
// version.
type otherVersionEndpoint struct {
	remote.Endpoint
	version string
}

func (e *otherVersionEndpoint) Hello(ctx context.Context) (remote.HostInfo, error) {
	hi, err := e.Endpoint.Hello(ctx)
	hi.Version = e.version
	return hi, err
}

// TestCompareConfigRemoteRefusesAVersionMismatch pins the compare-config
// half of R-P3-28c: preflight refuses a peer running a different
// claude-teleport (exit 4, spec §5) because the wire shapes differ, but
// compare-config dialled the same peer and happily rendered a drift table
// built from inventories the two versions do not agree on. It must apply
// the same gate — exit 4, and never a spurious drift row.
func TestCompareConfigRemoteRefusesAVersionMismatch(t *testing.T) {
	remoteEnv, _ := testEnv(t)
	target, opts, localHome := remoteHostExec(t, func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
		p, err := envPaths(remoteEnv)
		if err != nil {
			io.WriteString(stderr, err.Error())
			return 1
		}
		lopts, closeProbe := serverLocalOptions(context.Background(), remoteEnv, func(string, ...any) {})
		defer closeProbe()
		ep := &otherVersionEndpoint{Endpoint: remote.NewLocal(p, "claude-teleport", lopts), version: "0.0.0-other"}
		if err := remote.Serve(context.Background(), stdin, stdout, ep); err != nil {
			io.WriteString(stderr, err.Error())
			return 1
		}
		return 0
	})
	writeSettings(t, filepath.Join(localHome, ".claude"), `{}`)
	localEnv := []string{"HOME=" + localHome, "USER=alice", "PWD=" + localHome, "PATH=/usr/bin:/bin"}

	var out, errOut bytes.Buffer
	code := Main(append([]string{"compare-config", target}, opts...), strings.NewReader(""), &out, &errOut, localEnv)
	if code != ExitUnreachable {
		t.Fatalf("version mismatch = exit %d, want %d\nstdout: %s\nstderr: %s", code, ExitUnreachable, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "0.0.0-other") {
		t.Errorf("the failure must name both versions:\n%s", errOut.String())
	}
	if strings.Contains(out.String(), "differences") || strings.Contains(out.String(), "block") {
		t.Errorf("no drift table may be rendered for a mismatched peer:\n%s", out.String())
	}
}
