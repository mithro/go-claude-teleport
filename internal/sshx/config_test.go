package sshx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// decodeWarn decodes b and collects the warnings DecodeConfig emitted.
func decodeWarn(t *testing.T, b, home string) (get func(alias, key string) string, warns []string, err error) {
	t.Helper()
	path := filepath.Join(home, ".ssh", "config")
	cfg, err := DecodeConfig([]byte(b), path, home, func(f string, a ...any) {
		warns = append(warns, strings.TrimSpace(fmt.Sprintf(f, a...)))
	})
	get = func(alias, key string) string {
		if cfg == nil {
			return ""
		}
		v, err := cfg.Get(alias, key)
		if err != nil {
			return ""
		}
		return v
	}
	return get, warns, err
}

// The config from issue #21: a Match exec block anywhere in the file must not
// stop the rest of the file from resolving the target host.
func TestDecodeConfigDropsUnsupportedMatchBlock(t *testing.T) {
	const cfg = "Match exec \"true\"\n" +
		"\tStrictHostKeyChecking accept-new\n" +
		"\n" +
		"Host example\n" +
		"\tHostName example.invalid\n" +
		"\tUser someone\n"

	get, warns, err := decodeWarn(t, cfg, t.TempDir())
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if got := get("example", "HostName"); got != "example.invalid" {
		t.Errorf("HostName = %q, want %q", got, "example.invalid")
	}
	if got := get("example", "User"); got != "someone" {
		t.Errorf("User = %q, want %q", got, "someone")
	}
	if len(warns) != 1 {
		t.Fatalf("warnings = %q, want exactly one", warns)
	}
	for _, want := range []string{"Match", "exec", "line 1"} {
		if !strings.Contains(warns[0], want) {
			t.Errorf("warning %q does not mention %q", warns[0], want)
		}
	}
}

// A dropped block's settings must not leak into the resolved config: we could
// not evaluate the guard, so the block is treated as not applying.
func TestDecodeConfigDroppedMatchSettingsDoNotApply(t *testing.T) {
	const cfg = "Match exec \"true\"\n" +
		"\tStrictHostKeyChecking accept-new\n" +
		"\tUser fromtheblock\n" +
		"\nHost example\n\tHostName example.invalid\n"

	get, _, err := decodeWarn(t, cfg, t.TempDir())
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if got := get("example", "StrictHostKeyChecking"); got == "accept-new" {
		t.Errorf("StrictHostKeyChecking = %q, want the dropped block not to apply", got)
	}
	if got := get("example", "User"); got == "fromtheblock" {
		t.Errorf("User = %q, want the dropped block not to apply", got)
	}
}

// The two criteria the parser does implement keep working untouched.
func TestDecodeConfigKeepsSupportedMatchBlocks(t *testing.T) {
	const cfg = "Match all\n" +
		"\tServerAliveInterval 30\n" +
		"\nMatch host example\n" +
		"\tUser matched\n" +
		"\nHost example\n\tHostName example.invalid\n"

	get, warns, err := decodeWarn(t, cfg, t.TempDir())
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %q, want none", warns)
	}
	if got := get("example", "ServerAliveInterval"); got != "30" {
		t.Errorf("ServerAliveInterval = %q, want %q", got, "30")
	}
	if got := get("example", "User"); got != "matched" {
		t.Errorf("User = %q, want %q", got, "matched")
	}
}

// Every Match criterion the parser rejects must be dropped, not just exec.
func TestDecodeConfigDropsEveryUnsupportedCriterion(t *testing.T) {
	for _, criteria := range []string{
		`exec "true"`, "user bob", "localuser bob", "originalhost example",
		"final", "canonical", "tagged t", "localnetwork 10.0.0.0/8",
		"!host example", "",
	} {
		t.Run(criteria, func(t *testing.T) {
			cfg := "Match " + criteria + "\n\tUser fromtheblock\n" +
				"\nHost example\n\tHostName example.invalid\n"
			get, warns, err := decodeWarn(t, cfg, t.TempDir())
			if err != nil {
				t.Fatalf("DecodeConfig: %v", err)
			}
			if got := get("example", "HostName"); got != "example.invalid" {
				t.Errorf("HostName = %q, want %q", got, "example.invalid")
			}
			if got := get("example", "User"); got == "fromtheblock" {
				t.Errorf("User = %q, want the dropped block not to apply", got)
			}
			if len(warns) == 0 {
				t.Error("want a warning naming the dropped block")
			}
		})
	}
}

// A compound guard such as "Match host x user y" is one the parser misreads as
// three host patterns rather than rejecting. Once some other block has sent us
// down the repair path, drop it too: applying a block to hosts named "user" is
// worse than not applying it.
func TestDecodeConfigDropsCompoundMatchGuard(t *testing.T) {
	const cfg = "Match exec \"true\"\n\tUser fromexec\n" +
		"\nMatch host example user bob\n\tUser fromcompound\n" +
		"\nHost example\n\tHostName example.invalid\n"

	get, warns, err := decodeWarn(t, cfg, t.TempDir())
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if got := get("example", "User"); got == "fromcompound" || got == "fromexec" {
		t.Errorf("User = %q, want neither Match block to apply", got)
	}
	if len(warns) != 2 {
		t.Errorf("warnings = %q, want one per dropped block", warns)
	}
}

// A Match block inside an Include'd file is parsed recursively by the library
// and fails just as hard, so it has to be dropped there too — and the rest of
// the included file must survive.
func TestDecodeConfigDropsUnsupportedMatchInIncludedFile(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	inc := filepath.Join(sshDir, "extra.conf")
	body := "Match exec \"true\"\n\tUser fromtheblock\n\nHost included\n\tHostName included.invalid\n"
	if err := os.WriteFile(inc, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := "Include " + inc + "\n\nHost example\n\tHostName example.invalid\n"
	get, warns, err := decodeWarn(t, cfg, home)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if got := get("example", "HostName"); got != "example.invalid" {
		t.Errorf("HostName(example) = %q, want %q", got, "example.invalid")
	}
	if got := get("included", "HostName"); got != "included.invalid" {
		t.Errorf("HostName(included) = %q, want %q", got, "included.invalid")
	}
	if len(warns) != 1 || !strings.Contains(warns[0], inc) {
		t.Errorf("warnings = %q, want one naming %s", warns, inc)
	}
}

// Parse failures that are not about Match criteria are real broken-config
// errors and must stay fatal — we only stop treating an unevaluable guard as
// a syntax error.
func TestDecodeConfigLeavesOtherParseErrorsAlone(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sshDir, "adir"), 0o700); err != nil {
		t.Fatal(err)
	}
	self := filepath.Join(sshDir, "self.conf")
	if err := os.WriteFile(self, []byte("Include "+self+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for name, cfg := range map[string]string{
		"include a directory": "Include " + filepath.Join(sshDir, "adir") + "\n",
		"self-including file": "Include " + self + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := decodeWarn(t, cfg, home); err == nil {
				t.Error("want an error, got nil")
			}
		})
	}
}

// A config the parser already accepts must come back byte-for-byte equivalent
// and silent: the repair path only runs when the decode actually failed.
func TestDecodeConfigCleanConfigIsUntouched(t *testing.T) {
	b, err := os.ReadFile("testdata/ssh_config")
	if err != nil {
		t.Fatal(err)
	}
	get, warns, err := decodeWarn(t, string(b), t.TempDir())
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %q, want none", warns)
	}
	if got := get("big-storage", "HostName"); got != "big-storage.example" {
		t.Errorf("HostName = %q, want %q", got, "big-storage.example")
	}
	if got := get("big-storage", "ProxyJump"); got != "jump" {
		t.Errorf("ProxyJump = %q, want %q", got, "jump")
	}
}
