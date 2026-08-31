package transfer

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// newTwoHosts builds a source manifest (from sourceTree) whose Dst paths live
// under a second host's home directory, and returns the manifest plus a
// staging dir on that "dest" host. Shared by Task 12 (stream_test.go), Task
// 13's staged(), and Task 15's tests so the two-host fixture is built exactly
// once.
//
// It threads the SAME sandboxed home that sourceTree returns through both
// PathMap.From and the on-disk file paths, exactly as bobHome/pm are used in
// manifest_test.go's TestBuildHashesRewrittenContent. An earlier draft of
// this helper instead re-derived a "source home" from files[0].Root via two
// filepath.Dir calls; because files[0].Root is sourceTree's cfg dir
// (home/.claude), that landed one directory too shallow (home's parent, not
// home itself) — still a boundary-valid PathMap.From so ApplyPath did not
// error, but every Dst then carried a spurious extra "alice" path segment,
// the same class of source/mapping mismatch Task 10's original fixture had.
func newTwoHosts(t *testing.T) (*Manifest, string) {
	t.Helper()
	_, home, files := sourceTree(t)
	bob := bobHome(home)
	pm := session.NewPathMap(session.Mapping{From: home, To: bob})
	m, err := Build(context.Background(), sid, session.ID(sid), "laptop.example", "big-storage.example", files, pm)
	if err != nil {
		t.Fatal(err)
	}
	m.TmpDir = t.TempDir()
	return m, filepath.Join(t.TempDir(), "staging")
}
