package cli

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/mithro/go-claude-teleport/internal/sshx/sshtest"
)

// remoteHost starts an sshtest server whose exec handler runs THIS cli's
// `remote serve` in-process with remoteEnv, and returns "host:port" + the
// -o flags the client needs (key file, accept-new).
func remoteHost(t *testing.T, remoteEnv []string) (string, []string, string) {
	t.Helper()
	localHome := filepath.Join(t.TempDir(), "home", "alice")
	os.MkdirAll(filepath.Join(localHome, ".ssh"), 0o700)
	keyPath, signer := sshtest.WriteKeyFile(t, filepath.Join(localHome, ".ssh"), "id_ed25519", "")
	srv := sshtest.New(t, sshtest.Options{
		Authorized: []ssh.PublicKey{signer.PublicKey()},
		Exec: func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
			f := strings.Fields(cmd)
			if len(f) < 3 || f[1] != "remote" {
				io.WriteString(stderr, "unexpected: "+cmd)
				return 127
			}
			return Main(f[1:], stdin, stdout, stderr, remoteEnv)
		},
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

func TestInspectHostResolvesRemotely(t *testing.T) {
	remoteEnv, remoteHome := testEnv(t)
	target, opts, localHome := remoteHost(t, remoteEnv)
	localEnv := []string{"HOME=" + localHome, "USER=alice", "PATH=/bin"}
	// no such session on the remote -> not-found from the remote resolver
	var out, errOut bytes.Buffer
	code := Main(append([]string{"inspect", tsid, "--host", target}, opts...), strings.NewReader(""), &out, &errOut, localEnv)
	if code == ExitOK || !strings.Contains(errOut.String(), "not") {
		t.Errorf("exit %d stderr %q", code, errOut.String())
	}
	// with a transcript on the remote, inspect lists it
	proj := filepath.Join(remoteHome, ".claude", "projects", "-home-bob-work")
	os.MkdirAll(proj, 0o700)
	os.WriteFile(filepath.Join(proj, tsid+".jsonl"), []byte(`{"type":"user","cwd":"/home/bob/work","sessionId":"`+tsid+`","version":"2.1.247","timestamp":"2026-08-27T10:00:00Z","message":{"role":"user","content":"hi"}}`+"\n"), 0o600)
	out.Reset()
	errOut.Reset()
	code = Main(append([]string{"inspect", tsid, "--host", target}, opts...), strings.NewReader(""), &out, &errOut, localEnv)
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), tsid+".jsonl") || !strings.Contains(out.String(), "/home/bob/work") {
		t.Errorf("inspect --host output:\n%s", out.String())
	}
}
