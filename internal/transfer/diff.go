package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/session"
)

type Status string

const (
	Absent           Status = "absent"
	PresentSame      Status = "present-same"
	StagedSame       Status = "staged-same"
	PresentDifferent Status = "present-different"
	FFCandidate      Status = "ff-candidate"
	StagedMismatch   Status = "staged-mismatch"
)

// StagedPath is stagingDir/<id>; the in-flight file is StagedPath+".part";
// dirs are recorded as StagedPath+".dir" and symlinks as StagedPath+".symlink".
func StagedPath(stagingDir string, id int) string {
	return filepath.Join(stagingDir, strconv.Itoa(id))
}

// HashFile streams path through sha256.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// stagedState reports whether a verified staged copy of e exists; it removes
// .part remnants and wrong-size staged files (returning mismatch=true).
func stagedState(stagingDir string, e Entry) (staged bool, mismatch bool, err error) {
	base := StagedPath(stagingDir, e.ID)
	if _, serr := os.Lstat(base + ".part"); serr == nil {
		if err := os.Remove(base + ".part"); err != nil {
			return false, false, fmt.Errorf("remove partial %s: %w", base+".part", err)
		}
		mismatch = true
	}
	switch {
	case e.IsDir():
		_, serr := os.Lstat(base + ".dir")
		if serr == nil {
			return true, mismatch, nil
		}
		if errors.Is(serr, os.ErrNotExist) {
			return false, mismatch, nil
		}
		return false, false, fmt.Errorf("stat staged %s: %w", base+".dir", serr)
	case e.IsSymlink():
		raw, serr := os.ReadFile(base + ".symlink")
		if serr == nil {
			return string(raw) == e.Symlink, mismatch, nil
		}
		if errors.Is(serr, os.ErrNotExist) {
			return false, mismatch, nil
		}
		return false, false, fmt.Errorf("read staged %s: %w", base+".symlink", serr)
	}
	st, serr := os.Lstat(base)
	if errors.Is(serr, os.ErrNotExist) {
		return false, mismatch, nil
	}
	if serr != nil {
		return false, false, fmt.Errorf("stat staged %s: %w", base, serr)
	}
	if st.Size() != e.Size || !st.Mode().IsRegular() {
		if err := os.Remove(base); err != nil {
			return false, false, fmt.Errorf("remove mismatched staged %s: %w", base, err)
		}
		return false, true, nil
	}
	return true, mismatch, nil
}

// ffPrefixCheck runs the authoritative fast-forward check once a staged copy
// exists: JSONL transcripts are compared record-wise (tolerating
// re-encoding, i.e. key order/whitespace changes); everything else is
// compared by exact byte prefix.
func ffPrefixCheck(dst, staged string) (bool, error) {
	if strings.HasSuffix(dst, ".jsonl") {
		return session.IsRecordPrefix(dst, staged)
	}
	return session.IsPrefix(dst, staged)
}

// Diff runs on the destination and classifies every entry.
func Diff(ctx context.Context, m *Manifest, stagingDir string) (map[int]Status, error) {
	out := make(map[int]Status, len(m.Entries))
	for _, e := range m.Entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		staged, mismatch, err := stagedState(stagingDir, e)
		if err != nil {
			return nil, err
		}
		st, err := os.Lstat(e.Dst)
		if errors.Is(err, os.ErrNotExist) {
			switch {
			case staged:
				out[e.ID] = StagedSame
			case mismatch:
				out[e.ID] = StagedMismatch
			default:
				out[e.ID] = Absent
			}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", e.Dst, err)
		}
		switch {
		case e.IsDir():
			if st.IsDir() {
				out[e.ID] = PresentSame
			} else {
				out[e.ID] = PresentDifferent
			}
		case e.IsSymlink():
			if st.Mode()&os.ModeSymlink == 0 {
				out[e.ID] = PresentDifferent
				continue
			}
			target, rerr := os.Readlink(e.Dst)
			if rerr != nil {
				return nil, fmt.Errorf("readlink %s: %w", e.Dst, rerr)
			}
			if target == e.Symlink {
				out[e.ID] = PresentSame
			} else {
				out[e.ID] = PresentDifferent
			}
		default:
			if !st.Mode().IsRegular() {
				out[e.ID] = PresentDifferent
				continue
			}
			sum, _, herr := HashFile(e.Dst)
			if herr != nil {
				return nil, fmt.Errorf("hash %s: %w", e.Dst, herr)
			}
			if sum == e.SHA256 {
				out[e.ID] = PresentSame
				continue
			}
			if !e.FFAllowed {
				out[e.ID] = PresentDifferent
				continue
			}
			if !staged {
				// No staged copy to check against yet: classify as a
				// candidate unconditionally (controller ruling 1 — no size
				// heuristic); re-checked authoritatively once staged.
				out[e.ID] = FFCandidate
				continue
			}
			ok, perr := ffPrefixCheck(e.Dst, StagedPath(stagingDir, e.ID))
			if perr != nil {
				return nil, fmt.Errorf("prefix check %s: %w", e.Dst, perr)
			}
			if ok {
				out[e.ID] = FFCandidate
			} else {
				out[e.ID] = PresentDifferent
			}
		}
	}
	return out, nil
}

// Need lists entry ids that must be sent given statuses (manifest order):
// absent, staged-mismatch, ff-candidate, and present-different for FFAllowed
// entries (only reachable after Blocking passed with --force). Need has no
// staging-dir parameter, so an ff-candidate that is ALREADY staged is listed
// again; Receive (Task 12) skips an entry whose verified staged copy exists,
// so the re-listing costs bandwidth for that one file only after a crash
// between staging and install.
func Need(m *Manifest, st map[int]Status) []int {
	var ids []int
	for _, e := range m.Entries {
		switch st[e.ID] {
		case Absent, StagedMismatch, FFCandidate:
			ids = append(ids, e.ID)
		case PresentDifferent:
			if e.FFAllowed {
				ids = append(ids, e.ID)
			}
		}
	}
	return ids
}

// Blocking lists entries whose status forbids install: present-different,
// unless force is set and the entry belongs to this session (FFAllowed).
func Blocking(m *Manifest, st map[int]Status, force bool) []Entry {
	var out []Entry
	for _, e := range m.Entries {
		if st[e.ID] == PresentDifferent && !(force && e.FFAllowed) {
			out = append(out, e)
		}
	}
	return out
}
