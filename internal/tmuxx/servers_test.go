package tmuxx

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDial treats any socket path in `live` as a running server.
func fakeDial(live map[string]bool) Dialer {
	return func(_ context.Context, p string) (Transport, error) {
		if live[p] {
			return &Fake{Default: []string{}}, nil
		}
		return nil, ErrNoServer
	}
}

func mkSocket(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	l, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
	return p
}

func TestFindServerOrder(t *testing.T) {
	dir := t.TempDir()
	main := mkSocket(t, dir, "main")
	def := mkSocket(t, dir, "default")
	other := mkSocket(t, dir, "other")
	restore := Dial
	defer func() { Dial = restore }()

	Dial = fakeDial(map[string]bool{main: true, def: true, other: true})
	if got, _ := FindServer(dir, "main", ""); got != main {
		t.Errorf("preferred name: %q, want %q", got, main)
	}
	if got, _ := FindServer(dir, "", ""); got != def {
		t.Errorf("default: %q, want %q", got, def)
	}
	if got, _ := FindServer(dir, "", "other"); got != other {
		t.Errorf("override: %q, want %q", got, other)
	}
	// Preferred dead, default dead, exactly one live → that one.
	Dial = fakeDial(map[string]bool{other: true})
	if got, err := FindServer(dir, "main", ""); got != other || err != nil {
		t.Errorf("single live: %q %v, want %q", got, err, other)
	}
	// Two live candidates, neither preferred nor default → error listing them.
	Dial = fakeDial(map[string]bool{other: true, main: true})
	_, err := FindServer(dir, "nope", "")
	if err == nil || !strings.Contains(err.Error(), "main") || !strings.Contains(err.Error(), "other") {
		t.Errorf("ambiguous: err = %v, want list of sockets", err)
	}
	// Override that is dead is an error, never a fallback.
	Dial = fakeDial(map[string]bool{main: true})
	if _, err := FindServer(dir, "", "other"); err == nil {
		t.Error("dead override must error")
	}
	// Nothing live at all.
	Dial = fakeDial(nil)
	if _, err := FindServer(dir, "", ""); !errors.Is(err, ErrNoServer) {
		t.Errorf("none: %v, want ErrNoServer", err)
	}
}

func TestListServersMissingDir(t *testing.T) {
	got, err := ListServers(filepath.Join(t.TempDir(), "absent"))
	if err != nil || len(got) != 0 {
		t.Errorf("got %v %v", got, err)
	}
	dir := t.TempDir()
	mkSocket(t, dir, "a")
	os.WriteFile(filepath.Join(dir, "notasocket"), nil, 0o600)
	got, err = ListServers(dir)
	if err != nil || len(got) != 1 || filepath.Base(got[0]) != "a" {
		t.Errorf("got %v %v", got, err)
	}
}
