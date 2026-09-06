package remote

import (
	"errors"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/bootstrap"
	"github.com/mithro/go-multi-binary/fatblob"
)

// fatImage builds a canonical fat image carrying arm64, for the "this build
// can bootstrap" branch. A plain []byte stands in for a non-fat dev build.
func fatImage(t *testing.T) []byte {
	t.Helper()
	blob := fatblob.Blob{Slices: []fatblob.Slice{
		{Arch: "amd64", Status: fatblob.StatusPresent, Data: []byte("native-amd64")},
		{Arch: "arm64", Status: fatblob.StatusPresent, Data: []byte("native-arm64")},
	}}
	img, err := fatblob.BuildCanonical([]byte("native-amd64"), blob)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

const bootPath = "/home/bob/.cache/claude-teleport/claude-teleport-9.9-arm64"

func okDeploy() func() (bootstrap.Result, error) {
	return func() (bootstrap.Result, error) {
		return bootstrap.Result{Arch: "arm64", RemotePath: bootPath}, nil
	}
}

// notCalledConnect fails if used — for cases that must not open a second
// (bootstrapped) connection.
func notCalledConnect(t *testing.T) connectFunc {
	return func(string) (*Client, error) { t.Helper(); t.Fatal("connect must not be called"); return nil, nil }
}

func TestDecideInstalledMatchingSkipsBootstrap(t *testing.T) {
	installed := &Client{info: HostInfo{Version: "9.9", Protocol: 3}}
	deployed := false
	got, err := decideClient(installed, nil, notCalledConnect(t),
		func() (bootstrap.Result, error) { deployed = true; return bootstrap.Result{}, nil },
		fatImage(t), "9.9", 3, true, "host", nil)
	if err != nil || got != installed {
		t.Fatalf("got %v %v, want the installed client", got, err)
	}
	if deployed {
		t.Error("a matching installed helper must not trigger a deploy")
	}
}

func TestDecideAbsentBootstrapsWhenFat(t *testing.T) {
	deployed := false
	bootstrapped := &Client{info: HostInfo{Version: "9.9", Protocol: 3}}
	connect := func(exe string) (*Client, error) {
		if exe != bootPath {
			t.Errorf("bootstrapped connect exe = %q, want %q", exe, bootPath)
		}
		return bootstrapped, nil
	}
	got, err := decideClient(nil, errors.New("claude-teleport: not found on PATH"), connect,
		func() (bootstrap.Result, error) { deployed = true; return okDeploy()() },
		fatImage(t), "9.9", 3, true, "host", nil)
	if err != nil || got != bootstrapped {
		t.Fatalf("got %v %v, want the bootstrapped client", got, err)
	}
	if !deployed {
		t.Error("absent helper on a fat build must deploy")
	}
}

func TestDecideAbsentNotFatReturnsActionableError(t *testing.T) {
	_, err := decideClient(nil, errors.New("claude-teleport: not found"), notCalledConnect(t),
		func() (bootstrap.Result, error) {
			t.Fatal("must not deploy a non-fat build")
			return bootstrap.Result{}, nil
		},
		[]byte("plain-dev-binary"), "9.9", 3, true, "host", nil)
	if err == nil || !strings.Contains(err.Error(), "not a fat binary") {
		t.Fatalf("err = %v, want a 'not a fat binary' hint", err)
	}
}

func TestDecideAbsentBootstrapDisabled(t *testing.T) {
	_, err := decideClient(nil, errors.New("not found"), notCalledConnect(t),
		func() (bootstrap.Result, error) { t.Fatal("disabled must not deploy"); return bootstrap.Result{}, nil },
		fatImage(t), "9.9", 3, false, "host", nil)
	if err == nil || !strings.Contains(err.Error(), "bootstrap disabled") {
		t.Fatalf("err = %v, want a 'bootstrap disabled' hint", err)
	}
}

func TestDecideMismatchedFatReplaces(t *testing.T) {
	installed := &Client{info: HostInfo{Version: "8.0", Protocol: 3}} // older; Close() is nil-safe
	deployed := false
	bootstrapped := &Client{info: HostInfo{Version: "9.9", Protocol: 3}}
	got, err := decideClient(installed, nil,
		func(exe string) (*Client, error) { return bootstrapped, nil },
		func() (bootstrap.Result, error) { deployed = true; return okDeploy()() },
		fatImage(t), "9.9", 3, true, "host", nil)
	if err != nil || got != bootstrapped {
		t.Fatalf("got %v %v, want the bootstrapped (matching) client", got, err)
	}
	if !deployed {
		t.Error("a version-mismatched installed helper on a fat build must be replaced")
	}
}

func TestDecideMismatchedNotFatKeepsInstalled(t *testing.T) {
	installed := &Client{info: HostInfo{Version: "8.0", Protocol: 3}}
	got, err := decideClient(installed, nil, notCalledConnect(t),
		func() (bootstrap.Result, error) { t.Fatal("non-fat must not deploy"); return bootstrap.Result{}, nil },
		[]byte("plain"), "9.9", 3, true, "host", nil)
	// Can't replace it; hand the mismatched client back for the caller to
	// report (unchanged pre-bootstrap behaviour).
	if err != nil || got != installed {
		t.Fatalf("got %v %v, want the installed client handed back", got, err)
	}
}
