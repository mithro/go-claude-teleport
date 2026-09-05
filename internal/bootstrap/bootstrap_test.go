package bootstrap

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/mithro/go-multi-binary/fatblob"
)

// fakeFat builds a canonical fat image carrying the given arch→native bytes,
// so Reconstruct(image, arch) yields native(arch)++blob deterministically.
func fakeFat(t *testing.T, natives map[string]string) []byte {
	t.Helper()
	var blob fatblob.Blob
	var first string
	for _, arch := range []string{"386", "amd64", "arm", "arm64", "riscv64"} {
		n, ok := natives[arch]
		if !ok {
			continue
		}
		if first == "" {
			first = n
		}
		blob.Slices = append(blob.Slices, fatblob.Slice{Arch: arch, Status: fatblob.StatusPresent, Data: []byte(n)})
	}
	img, err := fatblob.BuildCanonical([]byte(first), blob)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func md5hex(b []byte) string { s := md5.Sum(b); return hex.EncodeToString(s[:]) }

var quoted = regexp.MustCompile(`'((?:[^']|'\\'')*)'`)

// args returns the single-quoted tokens in a command, un-quoted.
func args(cmd string) []string {
	var out []string
	for _, m := range quoted.FindAllStringSubmatch(cmd, -1) {
		out = append(out, strings.ReplaceAll(m[1], `'\''`, "'"))
	}
	return out
}

// fakeRemote is an in-memory remote fs + shell just rich enough for Deploy.
type fakeRemote struct {
	uname string
	home  string
	fs    map[string][]byte
	cmds  []string
	puts  int
}

func newFakeRemote(uname, home string) *fakeRemote {
	return &fakeRemote{uname: uname, home: home, fs: map[string][]byte{}}
}

func (f *fakeRemote) run(_ context.Context, cmd string) (string, error) {
	f.cmds = append(f.cmds, cmd)
	switch {
	case cmd == "uname -m":
		return f.uname + "\n", nil
	case strings.Contains(cmd, "XDG_CACHE_HOME"):
		return f.home + "/.cache/claude-teleport", nil
	case strings.HasPrefix(cmd, "mkdir -p"), strings.HasPrefix(cmd, "chmod +x"):
		return "", nil
	case strings.HasPrefix(cmd, "test -f"):
		p := args(cmd)[0]
		if d, ok := f.fs[p]; ok {
			return md5hex(d) + "  " + p + "\n", nil
		}
		return "", nil // `|| true` path: file absent
	case strings.HasPrefix(cmd, "md5sum"):
		p := args(cmd)[0]
		d, ok := f.fs[p]
		if !ok {
			return "", fmt.Errorf("md5sum: %s: No such file or directory", p)
		}
		return md5hex(d) + "  " + p + "\n", nil
	case strings.HasPrefix(cmd, "mv -f"):
		a := args(cmd)
		f.fs[a[1]] = f.fs[a[0]]
		delete(f.fs, a[0])
		return "", nil
	case strings.HasPrefix(cmd, "rm -f"):
		delete(f.fs, args(cmd)[0])
		return "", nil
	}
	return "", fmt.Errorf("fakeRemote: unexpected command %q", cmd)
}

func (f *fakeRemote) put(_ context.Context, data []byte, path string) error {
	f.fs[path] = append([]byte(nil), data...)
	f.puts++
	return nil
}

func TestDeployReconstructsAndInstallsForRemoteArch(t *testing.T) {
	img := fakeFat(t, map[string]string{"amd64": "NATIVE-amd64-xxxxxxxx", "arm64": "NATIVE-arm64-yyyyyyyy"})
	fr := newFakeRemote("aarch64", "/home/bob")
	res, err := Deploy(context.Background(), fr.run, fr.put, img, "9.9")
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if res.Arch != "arm64" {
		t.Errorf("arch = %q, want arm64", res.Arch)
	}
	want := "/home/bob/.cache/claude-teleport/claude-teleport-9.9-arm64"
	if res.RemotePath != want {
		t.Errorf("path = %q, want %q", res.RemotePath, want)
	}
	// The installed bytes must be canonical(arm64): reconstruct independently.
	wantData, _ := fatblob.Reconstruct(img, "arm64")
	if got := fr.fs[want]; string(got) != string(wantData) {
		t.Errorf("installed bytes are not canonical(arm64)")
	}
	if res.MD5 != md5hex(wantData) {
		t.Errorf("md5 = %s, want %s", res.MD5, md5hex(wantData))
	}
	if res.Reused {
		t.Errorf("first deploy should not be a reuse")
	}
	// No temp file left behind.
	if _, ok := fr.fs[want+".incoming"]; ok {
		t.Errorf("temp file left behind")
	}
}

func TestDeployIsIdempotent(t *testing.T) {
	img := fakeFat(t, map[string]string{"amd64": "NATIVE-amd64", "arm64": "NATIVE-arm64"})
	fr := newFakeRemote("aarch64", "/home/bob")
	if _, err := Deploy(context.Background(), fr.run, fr.put, img, "9.9"); err != nil {
		t.Fatal(err)
	}
	puts := fr.puts
	res, err := Deploy(context.Background(), fr.run, fr.put, img, "9.9")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Reused {
		t.Errorf("second deploy should be a reuse")
	}
	if fr.puts != puts {
		t.Errorf("reuse re-uploaded: puts %d -> %d", puts, fr.puts)
	}
}

func TestDeployRejectsNonFatBinary(t *testing.T) {
	fr := newFakeRemote("aarch64", "/home/bob")
	_, err := Deploy(context.Background(), fr.run, fr.put, []byte("just a plain elf, no trailer"), "9.9")
	if err != ErrNotFatBinary {
		t.Fatalf("err = %v, want ErrNotFatBinary", err)
	}
	if len(fr.cmds) != 0 {
		t.Errorf("a non-fat binary must not touch the remote: %v", fr.cmds)
	}
}

func TestDeployRejectsUnsupportedRemoteArch(t *testing.T) {
	img := fakeFat(t, map[string]string{"amd64": "NATIVE-amd64", "arm64": "NATIVE-arm64"})
	fr := newFakeRemote("m68k", "/home/bob")
	if _, err := Deploy(context.Background(), fr.run, fr.put, img, "9.9"); err == nil {
		t.Fatal("want error for unsupported remote arch")
	}
	if fr.puts != 0 {
		t.Errorf("must not upload for an unsupported arch")
	}
}

func TestDeployFailsWhenArchNotCarried(t *testing.T) {
	// Fat image without arm64; remote is arm64.
	img := fakeFat(t, map[string]string{"amd64": "NATIVE-amd64", "386": "NATIVE-386"})
	fr := newFakeRemote("aarch64", "/home/bob")
	if _, err := Deploy(context.Background(), fr.run, fr.put, img, "9.9"); err == nil {
		t.Fatal("want error when the image does not carry the remote arch")
	}
}

func TestArchesAndIsFat(t *testing.T) {
	img := fakeFat(t, map[string]string{"amd64": "a", "arm64": "b", "386": "c"})
	if !IsFat(img) {
		t.Error("IsFat = false for a canonical image")
	}
	got, err := Arches(img)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "386,amd64,arm64" {
		t.Errorf("Arches = %v, want [386 amd64 arm64]", got)
	}
	if IsFat([]byte("plain")) {
		t.Error("IsFat = true for a plain binary")
	}
}
