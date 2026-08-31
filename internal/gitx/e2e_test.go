// internal/gitx/e2e_test.go
package gitx

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mithro/go-claude-teleport/internal/session"
)

func TestFreshMainRoundTripThroughFiles(t *testing.T) {
	base := t.TempDir()
	srcHome := filepath.Join(base, "home", "alice")
	dstHome := filepath.Join(base, "home", "bob")
	srcMain := filepath.Join(srcHome, "github", "x")
	initRepo(t, srcMain)
	w := addWorktree(t, srcMain, "feat")
	addWorktree(t, srcMain, "other")
	writeFile(t, filepath.Join(w, "work.go"), "package work\n")
	gitCLI(t, w, "add", "work.go")
	writeFile(t, filepath.Join(w, "scratch.txt"), "untracked\n")

	info, err := Inspect(w)
	if err != nil {
		t.Fatal(err)
	}
	pm := session.NewPathMap(session.Mapping{From: srcHome, To: dstHome})
	p, err := PlanTransfer(info, &DestState{}, pm)
	if err != nil {
		t.Fatal(err)
	}
	files, err := Files(p, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the transfer: every entry lands at pm.ApplyPath(Root)/Rel.
	for _, e := range files {
		dst := filepath.Join(pm.ApplyPath(e.Root), filepath.FromSlash(e.Rel))
		switch {
		case e.Mode.IsDir():
			if err := os.MkdirAll(dst, e.Mode.Perm()); err != nil {
				t.Fatal(err)
			}
		case e.Symlink != "":
			os.MkdirAll(filepath.Dir(dst), 0o755)
			if err := os.Symlink(e.Symlink, dst); err != nil {
				t.Fatal(err)
			}
		default:
			b, err := os.ReadFile(e.Path())
			if err != nil {
				t.Fatal(err)
			}
			os.MkdirAll(filepath.Dir(dst), 0o755)
			if err := os.WriteFile(dst, b, e.Mode.Perm()); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := Attach(context.Background(), p, "", nil); err != nil {
		t.Fatal(err)
	}
	dstW := p.DstWorktree
	if !strings.HasPrefix(dstW, dstHome) {
		t.Fatalf("DstWorktree %q not under %q", dstW, dstHome)
	}
	if diff := cmp.Diff(porcelain(t, w), porcelain(t, dstW)); diff != "" {
		t.Errorf("status (-src +dst):\n%s", diff)
	}
	list := gitCLI(t, p.DstMain, "worktree", "list", "--porcelain")
	if strings.Contains(list, "other") {
		t.Errorf("other worktree leaked into the destination:\n%s", list)
	}
	if strings.Contains(list, srcHome) {
		t.Errorf("source home leaked into destination metadata:\n%s", list)
	}
	di, err := Inspect(dstW)
	if err != nil {
		t.Fatal(err)
	}
	if di.Branch != "feat" || di.Head != info.Head || di.MainDir != p.DstMain {
		t.Errorf("dest Inspect = %+v", di)
	}
	gitCLI(t, p.DstMain, "fsck", "--no-dangling")
}

// TestAttachSameDirSecondRunIsNoOp covers the W == M case end to end
// (Inspect -> PlanTransfer -> WritePack/StagedBlobsOf -> Attach) and then
// re-runs Attach with the same inputs: the second call must be a no-op and
// leave `git status --porcelain` on the destination unchanged.
func TestAttachSameDirSecondRunIsNoOp(t *testing.T) {
	base := t.TempDir()
	srcHome := filepath.Join(base, "home", "alice")
	dstHome := filepath.Join(base, "home", "bob")
	srcMain := filepath.Join(srcHome, "github", "x")
	repo, root := initRepo(t, srcMain)
	writeFile(t, filepath.Join(srcMain, "b.txt"), "b\n")
	commitAll(t, repo, "second")
	// srcMain is clean (fully committed): the destination should land at
	// Tip with a clean checkout, so a second Attach is a true no-op
	// (alreadyAtTip short-circuits before the clean/branch checks).

	dstMain := filepath.Join(dstHome, "github", "x")
	if err := os.MkdirAll(filepath.Dir(dstMain), 0o755); err != nil {
		t.Fatal(err)
	}
	initRepoAt(t, dstMain, srcMain, root)

	info, err := Inspect(srcMain)
	if err != nil {
		t.Fatal(err)
	}
	ds, err := DestStateOf(dstMain, dstMain, "main")
	if err != nil {
		t.Fatal(err)
	}
	ds.BranchTipReachable, err = IsAncestor(srcMain, ds.BranchTip, info.Head)
	if err != nil {
		t.Fatal(err)
	}
	pm := session.NewPathMap(session.Mapping{From: srcHome, To: dstHome})
	p, err := PlanTransfer(info, ds, pm)
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != ModeExistingMain || p.Linked {
		t.Fatalf("expected W==M existing-main plan, got %+v", p)
	}
	p.StagedBlobs, err = StagedBlobsOf(srcMain, filepath.Join(srcMain, p.IndexRel), p.Tip)
	if err != nil {
		t.Fatal(err)
	}
	var pack bytes.Buffer
	if err := WritePack(context.Background(), srcMain, append([]string{p.Tip}, p.StagedBlobs...), p.HaveTips, &pack); err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir()
	packPath := filepath.Join(staging, "objects.pack")
	if err := os.WriteFile(packPath, pack.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	idxStaged := filepath.Join(staging, "index")
	b, err := os.ReadFile(filepath.Join(srcMain, p.IndexRel))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(idxStaged, b, 0o644); err != nil {
		t.Fatal(err)
	}
	dirty := map[string]string{filepath.Join(p.DstMain, p.IndexRel): idxStaged}

	if err := Attach(context.Background(), p, packPath, dirty); err != nil {
		t.Fatal(err)
	}
	first := porcelain(t, dstMain)
	if diff := cmp.Diff(porcelain(t, srcMain), first); diff != "" {
		t.Fatalf("status after first attach (-src +dst):\n%s", diff)
	}
	head1 := strings.TrimSpace(gitCLI(t, dstMain, "rev-parse", "HEAD"))

	// Second Attach with identical inputs: must be a no-op.
	if err := Attach(context.Background(), p, packPath, dirty); err != nil {
		t.Fatal(err)
	}
	second := porcelain(t, dstMain)
	if diff := cmp.Diff(first, second); diff != "" {
		t.Errorf("status changed on second attach (-first +second):\n%s", diff)
	}
	if head2 := strings.TrimSpace(gitCLI(t, dstMain, "rev-parse", "HEAD")); head2 != head1 {
		t.Errorf("HEAD moved on second attach: %s -> %s", head1, head2)
	}
	gitCLI(t, dstMain, "fsck", "--no-dangling")
}
