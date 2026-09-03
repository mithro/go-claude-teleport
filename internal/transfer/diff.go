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

	"github.com/mithro/go-claude-teleport/internal/job"
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

// deferrableCategory reports whether Entry.Deferred is meaningful for cat.
//
// Deferred is a SOURCE-controlled wire field that tells the destination to
// classify an entry by staging state alone, without ever looking at Dst.
// That is exactly right for the three categories that own their Dst by
// construction — the job's own pane capture (CatCapture, whose Dst is
// <jobID>/capture.txt) and existing-main git-attach's dirty index and
// worktree files (CatRepo/CatWorktree, which git-attach places itself with
// git's own semantics) — and a licence to overwrite anything at all for
// every other category. So the DESTINATION decides, from the category, not
// from the flag: for anything else the flag is ignored and Dst is compared
// as usual, which turns a smuggled ~/.bashrc entry into present-different
// and hence a Blocking collision (spec §7.4).
func deferrableCategory(cat session.Category) bool {
	switch cat {
	case session.CatCapture, session.CatRepo, session.CatWorktree:
		return true
	}
	return false
}

// dataDirFromStagingDir recovers the destination's own claude-teleport
// DataDir from a stagingDir argument, instead of adding a session.Paths
// parameter to Diff that every caller of it — including ones with nothing
// to do with this fix — would then have to thread through. Every real
// caller builds stagingDir as job.StagingDir(DataDir, jobID), i.e.
// "<DataDir>/staging/<jobID>" (internal/remote/local.go's stagingDir
// method, used identically for both ManifestDiff and Install), so
// stripping the last two path components recovers exactly DataDir.
// stagingDir is never wire data — the caller builds it from its own
// trusted configuration — so this is exactly as trustworthy as being
// handed DataDir directly, and it never depends on any wire-supplied job
// id lining up with anything: it is pure path arithmetic on a
// caller-constructed argument.
func dataDirFromStagingDir(stagingDir string) string {
	return filepath.Dir(filepath.Dir(filepath.Clean(stagingDir)))
}

// canonicalCaptureDst is the ONE legitimate Dst a manifest's CatCapture
// entry may ever name: job.Dir(dataDir, jobID)/capture.txt — exactly the
// path internal/orchestrate/steps.go's runCapture builds on the source
// side. jobID is re-validated with job.ValidateID even though callers
// already check it at the dispatch boundary (internal/remote/jobid.go):
// this function itself joins it straight into a filesystem path, so it
// re-derives the safety property rather than trusting it was already
// applied.
func canonicalCaptureDst(dataDir, jobID string) (string, error) {
	if err := job.ValidateID(jobID); err != nil {
		return "", fmt.Errorf("capture entry: %w", err)
	}
	return filepath.Join(job.Dir(dataDir, jobID), "capture.txt"), nil
}

// Diff runs on the destination and classifies every entry.
func Diff(ctx context.Context, m *Manifest, stagingDir string) (map[int]Status, error) {
	out := make(map[int]Status, len(m.Entries))
	// Ruling R-P3-B1b (the B1 residual hole): a hostile or buggy source
	// can set a CatCapture entry's Dst to any path under Home (e.g.
	// ~/.bashrc), mark it Deferred, and stage attacker-chosen bytes under
	// that entry's id — Diff's Deferred branch below would then classify
	// it staged-same purely from staging state, WITHOUT ever comparing
	// Dst, and Install would rename the staged bytes straight over it.
	// The destination re-derives the only legitimate capture Dst itself,
	// once, up front, and the per-entry check below refuses any other Dst
	// unconditionally — before the Deferred short-circuit, before any
	// Lstat of Dst, regardless of what (if anything) already lives there.
	captureDst, captureDstErr := canonicalCaptureDst(dataDirFromStagingDir(stagingDir), m.JobID)
	for _, e := range m.Entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if e.Category == session.CatCapture && (captureDstErr != nil || filepath.Clean(e.Dst) != captureDst) {
			out[e.ID] = PresentDifferent
			continue
		}
		staged, mismatch, err := stagedState(stagingDir, e)
		if err != nil {
			return nil, err
		}
		stagedStatus := func() Status {
			switch {
			case staged:
				return StagedSame
			case mismatch:
				return StagedMismatch
			default:
				return Absent
			}
		}
		if e.Deferred && deferrableCategory(e.Category) {
			// A deferred entry is never compared against Dst: Dst is
			// whatever the destination's own current checkout already
			// holds there (an existing-main teleport's whole premise), not
			// a prior install of THIS entry, so a "difference" there is
			// meaningless — only "is the source's copy correctly staged
			// for the later step to read" matters.
			out[e.ID] = stagedStatus()
			continue
		}
		st, err := os.Lstat(e.Dst)
		if errors.Is(err, os.ErrNotExist) {
			out[e.ID] = stagedStatus()
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

// Pending lists entry ids with no verified staged (or better) copy yet:
// Absent and StagedMismatch only. Unlike Need — deliberately over-inclusive
// so a re-send of an already-staged ff-candidate is never skipped by
// mistake — Pending is the completeness check: an ff-candidate, a
// present-same/staged-same, or a present-different FFAllowed entry (which,
// by Diff's construction, is reachable only once a verified staged copy
// already failed its ff-prefix check) all already have everything the
// later install/git-attach step needs staged; go/claude-teleport's own
// transfer step is done at that point even though Need would still list
// some of them.
//
// Caveat (carry-6): an ff-candidate is assigned that Status unconditionally
// while nothing is staged yet, so before any pump has run Pending cannot
// tell an optimistic ff-candidate from a staged-and-verified one and a
// caller must gate on having attempted the transfer at least once —
// internal/orchestrate/steps.go's verifyTransfer does exactly that with its
// `Step("transfer").Attempts == 0` guard.
func Pending(m *Manifest, st map[int]Status) []int {
	var ids []int
	for _, e := range m.Entries {
		switch st[e.ID] {
		case Absent, StagedMismatch:
			ids = append(ids, e.ID)
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
