package session

import (
	"path/filepath"
	"testing"
)

func TestIsRecordPrefix(t *testing.T) {
	dir := t.TempDir()
	const (
		r1 = `{"type":"user","cwd":"/home/alice/p"}`
		r2 = `{"type":"assistant","n":1.5}`
		r3 = `{"type":"user","cwd":"/home/alice/q"}`
	)
	base := filepath.Join(dir, "base")
	mustWrite(t, base, r1+"\n"+r2+"\n")

	identical := filepath.Join(dir, "identical")
	mustWrite(t, identical, r1+"\n"+r2+"\n")

	extraTrailing := filepath.Join(dir, "extra-trailing")
	mustWrite(t, extraTrailing, r1+"\n"+r2+"\n"+r3+"\n")

	reencoded := filepath.Join(dir, "reencoded")
	mustWrite(t, reencoded, `{ "cwd": "/home/alice/p",   "type": "user" }`+"\n"+r2+"\n")

	diverged := filepath.Join(dir, "diverged")
	mustWrite(t, diverged, r1+"\n"+`{"type":"assistant","n":1.6}`+"\n")

	shorter := filepath.Join(dir, "shorter")
	mustWrite(t, shorter, r1+"\n")

	unparseableSame := filepath.Join(dir, "unparseable-same")
	mustWrite(t, unparseableSame, "not json at all\n"+r2+"\n")

	unparseableExisting := filepath.Join(dir, "unparseable-existing")
	mustWrite(t, unparseableExisting, "not json at all\n"+r2+"\n")

	unparseableDiff := filepath.Join(dir, "unparseable-diff")
	mustWrite(t, unparseableDiff, "different unparseable text\n"+r2+"\n")

	empty := filepath.Join(dir, "empty")
	mustWrite(t, empty, "")

	for _, tc := range []struct {
		name, existing, incoming string
		want                     bool
	}{
		{"identical", base, identical, true},
		{"identical to itself", base, base, true},
		{"incoming has extra trailing records", base, extraTrailing, true},
		{"re-encoded with different key order/whitespace", base, reencoded, true},
		{"a diverged record", base, diverged, false},
		{"existing longer than incoming", base, shorter, false},
		{"empty existing is always a prefix", empty, base, true},
		{"unparseable line falls back to exact byte comparison: equal", unparseableExisting, unparseableSame, true},
		{"unparseable line falls back to exact byte comparison: differs", unparseableExisting, unparseableDiff, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := IsRecordPrefix(tc.existing, tc.incoming)
			if err != nil {
				t.Fatalf("IsRecordPrefix: %v", err)
			}
			if got != tc.want {
				t.Errorf("IsRecordPrefix(%s, %s) = %v, want %v", filepath.Base(tc.existing), filepath.Base(tc.incoming), got, tc.want)
			}
		})
	}

	if _, err := IsRecordPrefix(base, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing incoming must be an error")
	}
	if _, err := IsRecordPrefix(filepath.Join(dir, "missing"), base); err == nil {
		t.Fatal("missing existing must be an error")
	}
}
