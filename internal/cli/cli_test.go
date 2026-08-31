package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// errWriter always fails to write, so a command that writes its result
// straight to a.stdout (e.g. version) returns a plain, non-*ExitError error.
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("write boom") }

func run(t *testing.T, env []string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = Main(args, strings.NewReader(""), &out, &errb, env)
	return code, out.String(), errb.String()
}

func TestVersionCommand(t *testing.T) {
	code, out, _ := run(t, nil, "version")
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if !strings.HasPrefix(out, "claude-teleport dev (protocol 2)") {
		t.Fatalf("stdout = %q", out)
	}
}

func TestUnknownFlagIsUsageError(t *testing.T) {
	code, _, stderr := run(t, nil, "--definitely-not-a-flag")
	if code != ExitUsage {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stderr, "claude-teleport:") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestPlainErrorExitsFailed(t *testing.T) {
	var errb bytes.Buffer
	code := Main([]string{"version"}, strings.NewReader(""), errWriter{}, &errb, nil)
	if code != ExitFailed {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
	if !strings.Contains(errb.String(), "claude-teleport:") {
		t.Fatalf("stderr = %q", errb.String())
	}
}

func TestExitErrorCodePropagates(t *testing.T) {
	err := Exit(ExitRefused, "drift on %s", "hooks")
	var ee *ExitError
	if !asExit(err, &ee) || ee.Code != ExitRefused || ee.Error() != "drift on hooks" {
		t.Fatalf("got %#v", err)
	}
}
