package claudecfg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// A settings.json that declares no hook must hash exactly as a
// settings.json with no "hooks" key at all.
//
// Observed on a real teleport (x1c-work -> desktop, 2026-09-14): the source
// carried `"hooks": {}` and the destination had no hooks key, so HooksHash
// was sha256("{}") = 44136fa355b3 on one side and "" on the other. That is
// the ONLY Block-class row compare produces for an otherwise-identical
// pair, so every teleport between those two machines demanded
// --allow-config-drift for a difference that changes nothing: neither host
// runs a hook.
func TestEmptyHooksHashAsAbsent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		hooks string
	}{
		{"empty object", `{}`},
		{"null", `null`},
		{"event with no matchers", `{"PreToolUse":[]}`},
		{"several empty events", `{"PreToolUse":[],"PostToolUse":[],"Stop":null}`},
		{"nested empties", `{"PreToolUse":[[],{}],"Stop":[null]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, "settings.json"), `{"hooks":`+tc.hooks+`}`)
			inv, err := Collect(session.NewPaths("/home/alice", dir, "/tmp/x", true), cwd, "h", "")
			if err != nil {
				t.Fatal(err)
			}
			if inv.HooksHash != "" {
				t.Errorf("HooksHash = %q for hooks %s, want \"\" (same as absent)", inv.HooksHash, tc.hooks)
			}
		})
	}
}

// Where the rule deliberately stops. `{"matcher":"Bash","hooks":[]}` runs
// no hook either, but seeing that requires knowing that "hooks" is the key
// that matters inside a matcher entry and "matcher" is not — i.e. encoding
// Claude Code's hook schema into this comparison, which would then rot
// silently the next time the schema moves. declaresNothing stays
// structural: a value is empty when it holds nothing but empty containers.
// The cost is a spurious block on a settings file nobody writes by hand;
// the alternative cost is silently skipping a real hook difference.
func TestMatcherWithNoHooksStillHashes(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "settings.json"),
		`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[]}]}}`)
	got, err := Collect(session.NewPaths("/home/alice", dir, "/tmp/x", true), cwd, "h", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.HooksHash == "" {
		t.Error("a matcher entry carrying a non-empty string hashed as absent; " +
			"declaresNothing has become schema-aware — see the comment above")
	}
}

// The flip side: a hooks block that actually declares a hook must still
// hash, and must still hash the CONTENT — this fix must not turn the
// blocking check off, only stop it firing on nothing.
func TestRealHooksStillHash(t *testing.T) {
	const real = `{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/bin/guard"}]}]}`
	// canonical() encodes through a Go map, which sorts object keys, so the
	// expected hash is over the SORTED spelling of the same content.
	const sorted = `{"PreToolUse":[{"hooks":[{"command":"/bin/guard","type":"command"}],"matcher":"Bash"}]}`
	dir := t.TempDir()
	write(t, filepath.Join(dir, "settings.json"), `{"hooks":`+real+`}`)
	inv, err := Collect(session.NewPaths("/home/alice", dir, "/tmp/x", true), cwd, "h", "")
	if err != nil {
		t.Fatal(err)
	}
	if inv.HooksHash != configHash(sorted) {
		t.Errorf("HooksHash = %q, want the hash of the hooks content %q", inv.HooksHash, configHash(sorted))
	}
	// A different command must still be a different hash, or the Block
	// class stops protecting anything.
	const other = `{"PreToolUse":[{"hooks":[{"command":"/bin/other","type":"command"}],"matcher":"Bash"}]}`
	if inv.HooksHash == configHash(other) {
		t.Error("two different hook commands hash the same")
	}
}

// End to end, the pair that forced --allow-config-drift on every teleport
// between the two real machines: `"hooks": {}` on the source, no hooks key
// on the destination. Compare must find nothing to say.
func TestCompareNoHooksRowForEmptyVsAbsent(t *testing.T) {
	collect := func(settings string) *Inventory {
		t.Helper()
		dir := t.TempDir()
		write(t, filepath.Join(dir, "settings.json"), settings)
		got, err := Collect(session.NewPaths("/home/alice", dir, "/tmp/x", true), cwd, "h", "")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	src := collect(`{"hooks":{},"model":"opus"}`)
	dst := collect(`{"model":"opus"}`)

	r := Compare(src, dst, nil)
	for _, d := range r.Diffs {
		if d.Key == "hooks" {
			t.Errorf("hooks row %+v for two hosts that both run no hook", d)
		}
	}
	if r.Blocking {
		t.Errorf("blocking drift between two hook-less hosts: %+v", r.Diffs)
	}
}

// A plugin's hooks/hooks.json is compared the same Block-class way, so an
// empty one has to behave the same as a missing one.
func TestEmptyPluginHooksFileHashesAsAbsent(t *testing.T) {
	dir := t.TempDir()
	install := filepath.Join(dir, "plugins", "repos", "m", "p")
	if err := os.MkdirAll(filepath.Join(install, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(install, "hooks", "hooks.json"), "{\n  \"PreToolUse\": []\n}\n")
	if err := os.MkdirAll(filepath.Join(dir, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	path, err := json.Marshal(install)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "plugins", "installed_plugins.json"),
		`{"plugins":{"p@m":[{"version":"1.0.0","installPath":`+string(path)+`}]}}`)

	got, err := Collect(session.NewPaths("/home/alice", dir, "/tmp/x", true), cwd, "h", "")
	if err != nil {
		t.Fatal(err)
	}
	if h := got.Plugins["p@m"].HooksHash; h != "" {
		t.Errorf("plugin HooksHash = %q for a hooks.json declaring nothing, want \"\"", h)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
