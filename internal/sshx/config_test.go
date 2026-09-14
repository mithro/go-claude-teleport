package sshx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// notAllowed is a command no allow list here carries, so a guard using it
// stands for "the parser cannot decide this block".
const notAllowed = `curl https://evil.example`

// decodeOpts decodes b and collects the warnings DecodeConfig emitted.
func decodeOpts(t *testing.T, b string, o ConfigOptions) (get func(alias, key string) string, warns []string, err error) {
	t.Helper()
	if o.Home == "" {
		o.Home = t.TempDir()
	}
	if o.Path == "" {
		o.Path = filepath.Join(o.Home, ".ssh", "config")
	}
	o.Warnf = func(f string, a ...any) {
		warns = append(warns, strings.TrimSpace(fmt.Sprintf(f, a...)))
	}
	cfg, err := DecodeConfig([]byte(b), o)
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

func decodeWarn(t *testing.T, b, home string) (func(alias, key string) string, []string, error) {
	t.Helper()
	return decodeOpts(t, b, ConfigOptions{Home: home})
}

// The shape from issue #21: a Match block the parser cannot decide must not
// stop the rest of the file from resolving the target host.
func TestDecodeConfigDropsUnsupportedMatchBlock(t *testing.T) {
	cfg := "Match exec \"" + notAllowed + "\"\n" +
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
	for _, want := range []string{"Match", "exec", "line 1", "allow list"} {
		if !strings.Contains(warns[0], want) {
			t.Errorf("warning %q does not mention %q", warns[0], want)
		}
	}
}

// A dropped block's settings must not leak into the resolved config.
func TestDecodeConfigDroppedMatchSettingsDoNotApply(t *testing.T) {
	cfg := "Match exec \"" + notAllowed + "\"\n" +
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

// The two criteria the parser implements itself keep working untouched.
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

// Every Match criterion the parser rejects and we cannot evaluate is dropped.
func TestDecodeConfigDropsEveryUnsupportedCriterion(t *testing.T) {
	for _, criteria := range []string{
		"user bob", "localuser bob", "originalhost example",
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
	cfg := "Match exec \"" + notAllowed + "\"\n\tUser fromexec\n" +
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
// and fails just as hard, so it has to be handled there too — and the rest of
// the included file must survive.
func TestDecodeConfigDropsUnsupportedMatchInIncludedFile(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	inc := filepath.Join(sshDir, "extra.conf")
	body := "Match exec \"" + notAllowed + "\"\n\tUser fromtheblock\n" +
		"\nHost included\n\tHostName included.invalid\n"
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
// errors and must stay fatal.
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

// A config the parser already accepts must come back unchanged and silent.
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

// --- Match exec ------------------------------------------------------------

// An allow-listed guard that exits 0 means the block applies, exactly as it
// would under OpenSSH, and says nothing.
func TestDecodeConfigAllowedMatchExecTrueAppliesBlock(t *testing.T) {
	const cfg = "Match exec \"true\"\n\tUser fromtheblock\n" +
		"\nHost example\n\tHostName example.invalid\n\tUser fallback\n"

	get, warns, err := decodeWarn(t, cfg, t.TempDir())
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if got := get("example", "User"); got != "fromtheblock" {
		t.Errorf("User = %q, want the evaluated block to apply", got)
	}
	if got := get("example", "HostName"); got != "example.invalid" {
		t.Errorf("HostName = %q, want %q", got, "example.invalid")
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %q, want none for a guard we could decide", warns)
	}
}

// An allow-listed guard that exits non-zero means the block does not apply.
// That is a normal decision, not a shortcoming, so it is silent too.
func TestDecodeConfigAllowedMatchExecFalseSkipsBlock(t *testing.T) {
	const cfg = "Match exec \"false\"\n\tUser fromtheblock\n" +
		"\nHost example\n\tHostName example.invalid\n\tUser fallback\n"

	get, warns, err := decodeWarn(t, cfg, t.TempDir())
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if got := get("example", "User"); got != "fallback" {
		t.Errorf("User = %q, want the block skipped", got)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %q, want none for a guard we could decide", warns)
	}
}

// The exit status is the answer, so a real predicate decides the block.
func TestDecodeConfigMatchExecUsesExitStatus(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "present")
	if err := os.WriteFile(present, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ cmd, want string }{
		"file exists":         {"test -e " + present, "fromtheblock"},
		"file does not exist": {"test -e " + filepath.Join(dir, "absent"), "fallback"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := "Match exec \"" + tc.cmd + "\"\n\tUser fromtheblock\n" +
				"\nHost example\n\tHostName example.invalid\n\tUser fallback\n"
			get, warns, err := decodeWarn(t, cfg, t.TempDir())
			if err != nil {
				t.Fatalf("DecodeConfig: %v", err)
			}
			if got := get("example", "User"); got != tc.want {
				t.Errorf("User = %q, want %q", got, tc.want)
			}
			if len(warns) != 0 {
				t.Errorf("warnings = %q, want none", warns)
			}
		})
	}
}

// "!exec" inverts the guard.
func TestDecodeConfigNegatedMatchExec(t *testing.T) {
	const cfg = "Match !exec \"false\"\n\tUser fromtheblock\n" +
		"\nHost example\n\tHostName example.invalid\n\tUser fallback\n"

	get, warns, err := decodeWarn(t, cfg, t.TempDir())
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if got := get("example", "User"); got != "fromtheblock" {
		t.Errorf("User = %q, want the negated guard to hold", got)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %q, want none", warns)
	}
}

// exec combined with host keeps the host half, which the parser can evaluate
// per lookup, so the block still only applies to the hosts named.
func TestDecodeConfigMatchExecCombinedWithHost(t *testing.T) {
	const cfg = "Match host example exec \"true\"\n\tUser fromtheblock\n" +
		"\nHost example\n\tHostName example.invalid\n\tUser fallback\n" +
		"\nHost other\n\tHostName other.invalid\n\tUser otheruser\n"

	get, warns, err := decodeWarn(t, cfg, t.TempDir())
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if got := get("example", "User"); got != "fromtheblock" {
		t.Errorf("User(example) = %q, want the block to apply", got)
	}
	if got := get("other", "User"); got != "otheruser" {
		t.Errorf("User(other) = %q, want the block not to reach this host", got)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %q, want none", warns)
	}
}

// Anything the allow list does not carry is never run.
func TestDecodeConfigRefusesCommandOffTheAllowList(t *testing.T) {
	cfg := "Match exec \"" + notAllowed + "\"\n\tUser fromtheblock\n" +
		"\nHost example\n\tHostName example.invalid\n\tUser fallback\n"

	get, warns, err := decodeWarn(t, cfg, t.TempDir())
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if got := get("example", "User"); got != "fallback" {
		t.Errorf("User = %q, want the block skipped", got)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "allow list") || !strings.Contains(warns[0], "curl") {
		t.Errorf("warnings = %q, want one naming curl and the allow list", warns)
	}
}

// The command is run directly, never through a shell, so anything that would
// need one is refused rather than run with the character taken literally.
func TestDecodeConfigRefusesCommandsNeedingAShell(t *testing.T) {
	for name, cmd := range map[string]string{
		"pipe":           "true | false",
		"semicolon":      "true; false",
		"and":            "true && false",
		"substitution":   "test $(whoami) = root",
		"backtick":       "test `whoami` = root",
		"redirect":       "true > /tmp/x",
		"glob":           "test -e /etc/*.conf",
		"tilde":          "test -e ~/x",
		"percent token":  "test %h = example",
		"path-qualified": "/tmp/evil/true",
		"relative path":  "./true",
		// An unbalanced quote is caught while the guard itself is split,
		// before any command is looked at.
		"unbalanced quote": `test -e "/x`,
	} {
		t.Run(name, func(t *testing.T) {
			cfg := "Match exec \"" + cmd + "\"\n\tUser fromtheblock\n" +
				"\nHost example\n\tHostName example.invalid\n\tUser fallback\n"
			get, warns, err := decodeWarn(t, cfg, t.TempDir())
			if err != nil {
				t.Fatalf("DecodeConfig: %v", err)
			}
			if got := get("example", "User"); got != "fallback" {
				t.Errorf("User = %q, want the block skipped", got)
			}
			if len(warns) != 1 {
				t.Fatalf("warnings = %q, want exactly one", warns)
			}
			if !strings.Contains(warns[0], "Match") || !strings.Contains(warns[0], "line 1") {
				t.Errorf("warning %q should name the refused guard and its line", warns[0])
			}
		})
	}
}

// ExecAllow adds to the built-in list, and "none" turns the whole thing off.
func TestDecodeConfigExecAllowList(t *testing.T) {
	const cfg = "Match exec \"sleep 0\"\n\tUser fromtheblock\n" +
		"\nHost example\n\tHostName example.invalid\n\tUser fallback\n"

	get, warns, err := decodeOpts(t, cfg, ConfigOptions{ExecAllow: []string{"sleep"}})
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if got := get("example", "User"); got != "fromtheblock" {
		t.Errorf("User = %q, want the added command to be run", got)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %q, want none", warns)
	}

	// Without the opt-in, the same guard is refused.
	get, warns, err = decodeWarn(t, cfg, t.TempDir())
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if got := get("example", "User"); got != "fallback" {
		t.Errorf("User = %q, want sleep refused by default", got)
	}
	if len(warns) != 1 {
		t.Errorf("warnings = %q, want one", warns)
	}

	// "none" refuses even the built-ins.
	get, warns, err = decodeOpts(t, "Match exec \"true\"\n\tUser fromtheblock\n"+
		"\nHost example\n\tHostName example.invalid\n\tUser fallback\n",
		ConfigOptions{ExecAllow: []string{"none"}})
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if got := get("example", "User"); got != "fallback" {
		t.Errorf("User = %q, want every command refused", got)
	}
	if len(warns) != 1 {
		t.Errorf("warnings = %q, want one", warns)
	}
}

// A guard that never answers must not hang the teleport.
func TestDecodeConfigMatchExecTimesOut(t *testing.T) {
	const cfg = "Match exec \"sleep 30\"\n\tUser fromtheblock\n" +
		"\nHost example\n\tHostName example.invalid\n\tUser fallback\n"

	start := time.Now()
	get, warns, err := decodeOpts(t, cfg, ConfigOptions{
		ExecAllow:   []string{"sleep"},
		ExecTimeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("took %s, want the guard to be cut short", elapsed)
	}
	if got := get("example", "User"); got != "fallback" {
		t.Errorf("User = %q, want the block skipped", got)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "no answer") {
		t.Errorf("warnings = %q, want one about the timeout", warns)
	}
}
