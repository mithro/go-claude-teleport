package transfer

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// hostPaths is the destination's session.Paths for a sandboxed home, in
// the standard shape internal/cli's session.NewPaths produces.
func hostPaths(home string) session.Paths {
	return session.Paths{
		Home:       home,
		ConfigDir:  filepath.Join(home, ".claude"),
		GlobalJSON: filepath.Join(home, ".claude.json"),
		DataDir:    filepath.Join(home, ".local", "share", "claude-teleport"),
	}
}

// refusedIDs returns the manifest ids err refuses, failing if err is not a
// *RefusalError (ruling R-P3-B1e item 5: a refusal is its own verdict, not
// a status).
func refusedIDs(t *testing.T, err error) []int {
	t.Helper()
	var re *RefusalError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want a *RefusalError", err)
	}
	var ids []int
	for _, r := range re.Refusals {
		ids = append(ids, r.ID)
	}
	return ids
}

// destPaths derives the DESTINATION's session.Paths from a manifest built
// by newTwoHosts: every Dst there lives under <destHome>/.claude/…, so the
// config dir (and from it the home, global json and data dir) is recovered
// by walking entry 0's Dst up to its ".claude" component. Diff needs it
// (ruling R-P3-B1e item 5) for exactly the same reason Install always
// did: only the destination's own paths say what a manifest may write.
func destPaths(t *testing.T, m *Manifest) session.Paths {
	t.Helper()
	cfg := m.Entries[0].Dst
	for filepath.Base(cfg) != ".claude" {
		parent := filepath.Dir(cfg)
		if parent == cfg {
			t.Fatalf("no .claude component in %s", m.Entries[0].Dst)
		}
		cfg = parent
	}
	p := hostPaths(filepath.Dir(cfg))
	p.ConfigDir = cfg
	return p
}

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
