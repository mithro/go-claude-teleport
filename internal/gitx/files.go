package gitx

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// walkOptions controls one directory walk.
type walkOptions struct {
	root     string
	category session.Category
	skipDirs map[string]bool   // absolute dirs never entered
	excludes []string          // path.Match patterns on the slash-relative path
	matcher  gitignore.Matcher // nil = no ignore handling
}

// Files lists what to move for the plan (spec §8).
func Files(p *Plan, excludes []string, includeIgnored bool) ([]session.FileEntry, error) {
	switch p.Mode {
	case ModeNotRepo:
		return walk(walkOptions{root: p.SrcWorktree, category: session.CatWorktree, excludes: excludes})
	case ModeFreshMain:
		return filesFreshMain(p, excludes, includeIgnored)
	case ModeExistingMain:
		return filesExistingMain(p, excludes)
	}
	return nil, fmt.Errorf("gitx.Files: unknown mode %q", p.Mode)
}

func filesFreshMain(p *Plan, excludes []string, includeIgnored bool) ([]session.FileEntry, error) {
	info, err := Inspect(p.SrcWorktree)
	if err != nil {
		return nil, err
	}
	skip := map[string]bool{}
	for _, o := range info.OtherWorktrees {
		skip[o] = true
		// Also skip the .git/worktrees/<name> metadata directory by extracting name from gitdir.
		dotGitFile := filepath.Join(o, ".git")
		if gd, err := readGitdirFile(dotGitFile); err == nil {
			name := filepath.Base(gd)
			skip[filepath.Join(info.CommonDir, "worktrees", name)] = true
		}
	}
	var mainMatcher gitignore.Matcher
	if !includeIgnored {
		if mainMatcher, err = ignoreMatcher(p.SrcMain); err != nil {
			return nil, err
		}
	}
	entries, err := walk(walkOptions{root: p.SrcMain, category: session.CatRepo, skipDirs: skip, excludes: excludes, matcher: mainMatcher})
	if err != nil {
		return nil, err
	}
	if !p.Linked {
		return entries, nil
	}
	// A linked worktree usually lives under M/.worktrees/<n> and was just
	// walked as part of M — unless it lives elsewhere. Either way, list it
	// separately as CatWorktree with its own ignore rules, and drop any
	// duplicates from the main walk.
	var wtMatcher gitignore.Matcher
	if !includeIgnored {
		if wtMatcher, err = ignoreMatcher(p.SrcWorktree); err != nil {
			return nil, err
		}
	}
	wt, err := walk(walkOptions{root: p.SrcWorktree, category: session.CatWorktree, excludes: excludes, matcher: wtMatcher})
	if err != nil {
		return nil, err
	}
	var out []session.FileEntry
	for _, e := range entries {
		abs := e.Path()
		if abs == p.SrcWorktree || strings.HasPrefix(abs, p.SrcWorktree+string(filepath.Separator)) {
			continue
		}
		out = append(out, e)
	}
	return append(out, wt...), nil
}

func filesExistingMain(p *Plan, excludes []string) ([]session.FileEntry, error) {
	var rel []string
	rel = append(rel, p.Dirty.Staged...)
	rel = append(rel, p.Dirty.Modified...)
	rel = append(rel, p.Dirty.Untracked...)
	sort.Strings(rel)
	var out []session.FileEntry
	seen := map[string]bool{}
	for _, r := range rel {
		if seen[r] || excluded(r, excludes) {
			continue
		}
		seen[r] = true
		e, err := entryFor(p.SrcWorktree, r, session.CatWorktree)
		if err != nil {
			if os.IsNotExist(err) {
				continue // staged then deleted from disk: nothing to carry
			}
			return nil, err
		}
		out = append(out, e)
	}
	idx, err := entryFor(p.SrcMain, p.IndexRel, session.CatRepo)
	if err != nil {
		return nil, fmt.Errorf("index file: %w", err)
	}
	return append(out, idx), nil
}

// ignoreMatcher reads every .gitignore under root plus .git/info/exclude.
func ignoreMatcher(root string) (gitignore.Matcher, error) {
	fsys := osfs.New(root)
	ps, err := gitignore.ReadPatterns(fsys, nil)
	if err != nil {
		return nil, fmt.Errorf("read .gitignore under %s: %w", root, err)
	}
	return gitignore.NewMatcher(ps), nil
}

func excluded(rel string, globs []string) bool {
	for _, g := range globs {
		if ok, _ := path.Match(g, rel); ok {
			return true
		}
		if ok, _ := path.Match(g, path.Base(rel)); ok {
			return true
		}
	}
	return false
}

func entryFor(root, rel string, cat session.Category) (session.FileEntry, error) {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	st, err := os.Lstat(abs)
	if err != nil {
		return session.FileEntry{}, err
	}
	e := session.FileEntry{Root: root, Rel: rel, Category: cat, Size: st.Size(), Mode: st.Mode(), ModTime: st.ModTime()}
	if st.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(abs)
		if err != nil {
			return session.FileEntry{}, err
		}
		e.Symlink = target
		e.Size = 0
	}
	if st.IsDir() {
		e.Size = 0
	}
	return e, nil
}

func walk(o walkOptions) ([]session.FileEntry, error) {
	var out []session.FileEntry
	err := filepath.WalkDir(o.root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if o.skipDirs[abs] {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(o.root, abs)
		if err != nil {
			return err
		}
		if rel == "." {
			rel = ""
		}
		rel = filepath.ToSlash(rel)
		if rel != "" {
			if excluded(rel, o.excludes) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			inGit := rel == ".git" || strings.HasPrefix(rel, ".git/")
			if o.matcher != nil && !inGit && o.matcher.Match(strings.Split(rel, "/"), d.IsDir()) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		e, err := entryFor(o.root, rel, o.category)
		if err != nil {
			return err
		}
		if e.Mode&(os.ModeSocket|os.ModeNamedPipe|os.ModeDevice|os.ModeCharDevice) != 0 {
			return nil // spec §7.1: sockets, fifos, devices are skipped (listed by the manifest builder)
		}
		out = append(out, e)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", o.root, err)
	}
	return out, nil
}
