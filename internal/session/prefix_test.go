package session

import (
	"path/filepath"
	"testing"
)

func TestIsPrefix(t *testing.T) {
	dir := t.TempDir()
	a, b, c, e := filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "c"), filepath.Join(dir, "e")
	mustWrite(t, a, "line1\nline2\n")
	mustWrite(t, b, "line1\nline2\nline3\n")
	mustWrite(t, c, "line1\nlineX\nline3\n")
	mustWrite(t, e, "")
	for _, tc := range []struct {
		existing, incoming string
		want               bool
	}{
		{a, b, true}, {a, a, true}, {b, a, false}, {a, c, false}, {e, a, true}, {a, e, false},
	} {
		got, err := IsPrefix(tc.existing, tc.incoming)
		if err != nil || got != tc.want {
			t.Errorf("IsPrefix(%s,%s) = %v %v want %v", filepath.Base(tc.existing), filepath.Base(tc.incoming), got, err, tc.want)
		}
	}
	if _, err := IsPrefix(a, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing incoming must be an error")
	}
}
