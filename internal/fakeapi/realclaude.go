package fakeapi

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
)

// spikeEnv is the exact environment ENDPOINTS.md was observed with. It drops
// every ambient CLAUDE_CODE_* variable (wildcard, not an enumerated list —
// Claude Code adds new ones across releases) plus CLAUDECODE, CLAUDE_PID and
// CLAUDE_EFFORT, then appends the fixed spike set below.
func spikeEnv(baseURL, configDir string) []string {
	drop := map[string]bool{
		"ANTHROPIC_BASE_URL": true, "ANTHROPIC_API_KEY": true, "CLAUDE_CONFIG_DIR": true,
		"CLAUDECODE": true, "CLAUDE_PID": true, "CLAUDE_EFFORT": true,
	}
	var env []string
	for _, kv := range os.Environ() {
		k := kv[:strings.IndexByte(kv+"=", '=')]
		if drop[k] || strings.HasPrefix(k, "CLAUDE_CODE_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"ANTHROPIC_BASE_URL="+baseURL,
		"ANTHROPIC_API_KEY=dummy-key",
		"CLAUDE_CONFIG_DIR="+configDir,
		"DISABLE_AUTOUPDATER=1",
		"DISABLE_TELEMETRY=1",
		"DISABLE_ERROR_REPORTING=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	)
}

// RunClaude runs the real `claude` binary against a fakeapi at baseURL with
// a throw-away config dir. It never reads ~/.claude or .credentials.json:
// CLAUDE_CONFIG_DIR points at configDir, which the caller creates fresh.
func RunClaude(ctx context.Context, baseURL, configDir, cwd string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = cwd
	cmd.Env = spikeEnv(baseURL, configDir)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	return out.Bytes(), errOut.Bytes(), err
}
