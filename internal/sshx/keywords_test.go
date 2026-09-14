package sshx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hostFor makes a config line that exercises kw without tripping the parser on
// the two keywords that need a well-formed argument.
func lineFor(kw string) string {
	switch strings.ToLower(kw) {
	case "host":
		return "Host example"
	case "match":
		return "Match all"
	case "include":
		return "Include /nonexistent/claude-teleport-probe/*"
	}
	return kw + " yes"
}

// Every keyword ssh_config(5) defines must be recognised. This is the promise
// the failure below rests on: refusing what we do not know is only reasonable
// if what we know is the whole set.
func TestDecodeConfigAcceptsEveryDefinedKeyword(t *testing.T) {
	for lower, canonical := range sshConfigKeywords {
		t.Run(canonical, func(t *testing.T) {
			if strings.ToLower(canonical) != lower {
				t.Errorf("table key %q does not match the spelling %q", lower, canonical)
			}
			cfg := "Host example\n\tHostName example.invalid\n\t" + lineFor(canonical) + "\n"
			if _, _, err := decodeWarn(t, cfg, t.TempDir()); err != nil {
				t.Errorf("DecodeConfig rejected the defined keyword %q: %v", canonical, err)
			}
		})
	}
}

// Everything the package claims to act on must be a real keyword.
func TestHonouredKeywordsAreAllDefined(t *testing.T) {
	for kw := range honouredKeywords {
		if _, ok := sshConfigKeywords[kw]; !ok {
			t.Errorf("honoured keyword %q is not in the defined set", kw)
		}
	}
}

// A keyword that is not an ssh_config keyword at all is an error, as it is
// under ssh itself, and the error says where it is and how to get past it.
func TestDecodeConfigRejectsUnknownKeyword(t *testing.T) {
	const cfg = "Host example\n\tHostName example.invalid\n\tBananaPhone yes\n"

	_, _, err := decodeWarn(t, cfg, t.TempDir())
	if err == nil {
		t.Fatal("want an error for an unknown keyword, got nil")
	}
	for _, want := range []string{"BananaPhone", "line 3", "IgnoreUnknown"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}
}

// All of them are named at once, not one per run.
func TestDecodeConfigNamesEveryUnknownKeyword(t *testing.T) {
	const cfg = "Host example\n\tBananaPhone yes\n\tKumquatMode 3\n\tHostName example.invalid\n"

	_, _, err := decodeWarn(t, cfg, t.TempDir())
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	for _, want := range []string{"BananaPhone", "KumquatMode"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}
}

// IgnoreUnknown in the config is OpenSSH's own override, and it works here.
func TestDecodeConfigIgnoreUnknownFromConfig(t *testing.T) {
	const cfg = "Host example\n\tIgnoreUnknown Banana*\n\tBananaPhone yes\n\tHostName example.invalid\n"

	get, _, err := decodeWarn(t, cfg, t.TempDir())
	if err != nil {
		t.Fatalf("IgnoreUnknown should have covered it: %v", err)
	}
	if got := get("example", "HostName"); got != "example.invalid" {
		t.Errorf("HostName = %q, want the rest of the config to be usable", got)
	}
}

// A pattern that does not cover the keyword leaves it an error.
func TestDecodeConfigIgnoreUnknownPatternMustMatch(t *testing.T) {
	const cfg = "Host example\n\tIgnoreUnknown Apple*\n\tBananaPhone yes\n\tHostName example.invalid\n"

	if _, _, err := decodeWarn(t, cfg, t.TempDir()); err == nil {
		t.Fatal("want an error: the pattern does not cover BananaPhone")
	}
}

// The same override from the command line, for when this build's table has
// gone stale against a newer OpenSSH and the config cannot be edited.
func TestDecodeConfigIgnoreUnknownFromOptions(t *testing.T) {
	const cfg = "Host example\n\tBananaPhone yes\n\tHostName example.invalid\n"

	if _, _, err := decodeOpts(t, cfg, ConfigOptions{IgnoreUnknown: []string{"*"}}); err != nil {
		t.Fatalf("-o IgnoreUnknown=* should have covered it: %v", err)
	}
	if _, _, err := decodeOpts(t, cfg, ConfigOptions{IgnoreUnknown: []string{"Banana*"}}); err != nil {
		t.Fatalf("a covering pattern should have worked: %v", err)
	}
	if _, _, err := decodeOpts(t, cfg, ConfigOptions{IgnoreUnknown: []string{"Apple*"}}); err == nil {
		t.Fatal("want an error: the pattern does not cover BananaPhone")
	}
}

// An Include'd file is part of the config, so its keywords are checked too and
// the error names that file rather than the one that pulled it in.
func TestDecodeConfigUnknownKeywordInIncludedFile(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	inc := filepath.Join(sshDir, "extra.conf")
	if err := os.WriteFile(inc, []byte("Host inc\n\tBananaPhone yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := "Include " + inc + "\n\nHost example\n\tHostName example.invalid\n"
	_, _, err := decodeWarn(t, cfg, home)
	if err == nil {
		t.Fatal("want an error for the unknown keyword in the included file")
	}
	if !strings.Contains(err.Error(), inc) || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("err = %v, want it to name %s line 2", err, inc)
	}
}

// Keyword matching is case-insensitive, as ssh_config is.
func TestDecodeConfigKeywordsAreCaseInsensitive(t *testing.T) {
	const cfg = "host example\n\thostname example.invalid\n\tSERVERALIVEINTERVAL 30\n"

	get, _, err := decodeWarn(t, cfg, t.TempDir())
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if got := get("example", "ServerAliveInterval"); got != "30" {
		t.Errorf("ServerAliveInterval = %q, want %q", got, "30")
	}
}

// The check covers the whole file, including a block that does not apply —
// ssh reads the file before it decides which blocks match, and so do we.
func TestDecodeConfigChecksKeywordsInBlocksThatDoNotApply(t *testing.T) {
	const cfg = "Match exec \"false\"\n\tBananaPhone yes\n\nHost example\n\tHostName example.invalid\n"

	if _, _, err := decodeWarn(t, cfg, t.TempDir()); err == nil {
		t.Fatal("want an error even though the block's guard is false")
	}
}
