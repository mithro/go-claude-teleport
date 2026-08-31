package gitx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/revlist"
)

// WritePack encodes the objects reachable from want but not from have into
// w as a packfile. Hashes in have that the repository does not contain are
// skipped (they are destination-only commits). When nothing is missing,
// nothing is written.
func WritePack(ctx context.Context, repoDir string, want []string, have []string, w io.Writer) error {
	repo, err := git.PlainOpenWithOptions(repoDir, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return fmt.Errorf("open %s: %w", repoDir, err)
	}
	s := repo.Storer
	var wantH, haveH []plumbing.Hash
	for _, h := range want {
		wantH = append(wantH, plumbing.NewHash(h))
	}
	for _, h := range have {
		ph := plumbing.NewHash(h)
		if err := s.HasEncodedObject(ph); err == nil {
			haveH = append(haveH, ph)
		}
	}
	hashes, err := revlist.Objects(s, wantH, haveH)
	if err != nil {
		return fmt.Errorf("revlist %s: %w", repoDir, err)
	}
	if len(hashes) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	enc := packfile.NewEncoder(w, s, false)
	if _, err := enc.Encode(hashes, 10); err != nil {
		return fmt.Errorf("encode pack (%d objects): %w", len(hashes), err)
	}
	return nil
}

// StagedBlobsOf returns the blob hashes in the index at indexPath that are
// not present in the tree of commit tip, sorted. repoDir is M (objects).
func StagedBlobsOf(repoDir, indexPath, tip string) ([]string, error) {
	f, err := os.Open(indexPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	idx := &index.Index{}
	if err := index.NewDecoder(f).Decode(idx); err != nil {
		return nil, fmt.Errorf("decode %s: %w", indexPath, err)
	}
	repo, err := git.PlainOpenWithOptions(repoDir, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", repoDir, err)
	}
	inTree := map[plumbing.Hash]bool{}
	if tip != "" {
		c, err := repo.CommitObject(plumbing.NewHash(tip))
		if err != nil {
			return nil, fmt.Errorf("commit %s: %w", tip, err)
		}
		tree, err := c.Tree()
		if err != nil {
			return nil, err
		}
		err = tree.Files().ForEach(func(f *object.File) error { inTree[f.Hash] = true; return nil })
		if err != nil {
			return nil, err
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range idx.Entries {
		if inTree[e.Hash] || seen[e.Hash.String()] {
			continue
		}
		seen[e.Hash.String()] = true
		out = append(out, e.Hash.String())
	}
	sort.Strings(out)
	return out, nil
}

// SourceFacts is what the source repository must answer before the plan
// can be made (it is computed on the source host by remote.GitSourceFacts).
type SourceFacts struct {
	DestTipReachable bool     // destTip is an ancestor of tip ("" destTip -> false)
	StagedBlobs      []string // StagedBlobsOf(mainDir, mainDir/indexRel, tip)
}

// SourceFactsOf combines IsAncestor and StagedBlobsOf. A destTip the
// source does not have is simply "not reachable" (a diverged branch).
func SourceFactsOf(mainDir, indexRel, tip, destTip string) (*SourceFacts, error) {
	f := &SourceFacts{}
	if destTip != "" && destTip != tip {
		ok, err := IsAncestor(mainDir, destTip, tip)
		if err == nil {
			f.DestTipReachable = ok
		} else if !errors.Is(err, plumbing.ErrObjectNotFound) {
			return nil, err
		}
	}
	if destTip == tip {
		f.DestTipReachable = true
	}
	blobs, err := StagedBlobsOf(mainDir, filepath.Join(mainDir, filepath.FromSlash(indexRel)), tip)
	if err != nil {
		return nil, err
	}
	f.StagedBlobs = blobs
	return f, nil
}
