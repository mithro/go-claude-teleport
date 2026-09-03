// internal/transfer/validate.go
package transfer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
)

// Refusal is one manifest entry the DESTINATION will not install, and why
// (ruling R-P3-B1e item 5). A refusal is not a content collision: a
// collision says "something different is already at this path, decide what
// to do"; a refusal says "this entry may never be written here at all".
type Refusal struct {
	ID       int              `json:"id"`
	Dst      string           `json:"dst"`
	Category session.Category `json:"category"`
	Reason   string           `json:"reason"`
}

func (r Refusal) String() string {
	if r.ID < 0 {
		if r.Dst == "" {
			return "refused: " + r.Reason
		}
		return fmt.Sprintf("refused: %s: %s", r.Dst, r.Reason)
	}
	return fmt.Sprintf("refused: entry %d %s (category %q): %s", r.ID, r.Dst, r.Category, r.Reason)
}

// RefusalError carries every refusal Diff found, so preflight can report
// them all at once rather than one per run.
type RefusalError struct{ Refusals []Refusal }

func (e *RefusalError) Error() string {
	if len(e.Refusals) == 1 {
		return e.Refusals[0].String()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d manifest entries refused by the destination:", len(e.Refusals))
	for _, r := range e.Refusals {
		fmt.Fprintf(&b, "\n  %s", r)
	}
	return b.String()
}

// Refuse builds a refusal for something that is not a manifest entry — a
// destination path a git plan named, say. dst may be empty.
func Refuse(dst, format string, a ...any) *RefusalError {
	return &RefusalError{Refusals: []Refusal{{ID: -1, Dst: dst, Reason: fmt.Sprintf(format, a...)}}}
}

// IsRefusal reports whether err is (or wraps) a RefusalError — the one
// predicate callers use to tell "this manifest may never be installed"
// apart from an I/O failure.
func IsRefusal(err error) bool {
	var re *RefusalError
	return errors.As(err, &re)
}

// allowedCategory is ruling R-P3-B1e item 1: the destination installs
// exactly the four categories spec §7.1 names, compared byte-for-byte.
// Everything else — a typo, an empty string, a case or whitespace variant,
// and "pack" (whose bytes never travel as a manifest entry: the git pack is
// its own stream, straight into gitx.Attach) — is refused before any other
// check, so no future category can inherit the old "well, it is under
// $HOME" fallback by accident.
func allowedCategory(cat session.Category) bool {
	switch cat {
	case session.CatSession, session.CatCapture, session.CatRepo, session.CatWorktree:
		return true
	}
	return false
}

// validatorMode selects how much of the check applies.
type validatorMode int

const (
	// modeDiff diagnoses (Diff): every rule is evaluated, nothing is
	// recorded — an absent Root is simply acceptable, because the Install
	// that later creates it is what claims it.
	modeDiff validatorMode = iota
	// modeInstall is the authoritative gate immediately before placement:
	// same rules, plus claiming an absent Root in jobs/<id>/roots.json
	// BEFORE anything is written under it.
	modeInstall
	// modeUninstall skips the Root rules entirely: Uninstall only ever
	// removes content some earlier Install placed (hash-verified at that
	// point), and it legitimately processes existing-main's Deferred dirty
	// entries, whose Root was never this job's to begin with.
	modeUninstall
)

// validator is the ONE implementation of "may the destination write this
// entry, and where". Diff runs it to diagnose (so a refusal surfaces at
// preflight, naming the entry and the reason) and Install runs it again as
// the authoritative gate, so the two can never drift apart and a hostile or
// stale status map cannot skip a single check.
type validator struct {
	p     session.Paths
	jobID string
	roots []Root
	mode  validatorMode

	home, configDir, dataDir string // EvalSymlinks-resolved, computed once
	captureDst               string
	captureErr               error

	recorded map[string]bool   // roots.json: RESOLVED roots THIS job created
	pending  map[string]bool   // resolved roots this pass will claim (N5)
	rootRes  map[string]string // declared root -> resolved path
	rootBad  map[string]string // declared root -> refusal reason ("" = fine)

	// claimedCwds is Munge(cwd) for every destination cwd the manifest's
	// OWN session entries claim: a CatSession Dst under
	// <ConfigDir>/projects/<munged>/… is the session saying "I ran in the
	// directory that munges to <munged>". Root.MayPreExist is honoured
	// only for a root the session claims this way (ruling R-P3-B1f N2).
	claimedCwds map[string]bool

	// placed accumulates what Install actually wrote, for
	// jobs/<id>/installed.json — the ONLY thing a later Uninstall/
	// DeleteInstalled may remove (ruling R-P3-B1f N3).
	placed []installedRecord
}

func newValidator(m *Manifest, p session.Paths, mode validatorMode) (*validator, error) {
	v := &validator{
		p: p, jobID: m.JobID, roots: m.Roots, mode: mode,
		recorded:    map[string]bool{},
		pending:     map[string]bool{},
		rootRes:     map[string]string{},
		rootBad:     map[string]string{},
		claimedCwds: manifestClaimedCwds(m, p),
	}
	// Every provenance record this validator consults or writes lives
	// under jobs/<id>/, so a manifest that does not name a usable job is
	// refused outright: the destination could neither attribute an
	// install to a job nor, later, justify a deletion by it.
	if err := job.ValidateID(m.JobID); err != nil {
		return nil, Refuse("", "manifest job id %q is not usable on this host: %v", m.JobID, err)
	}
	var err error
	if v.home, err = resolveExisting(p.Home); err != nil {
		return nil, fmt.Errorf("resolve home %s: %w", p.Home, err)
	}
	if v.configDir, err = resolveExisting(p.ConfigDir); err != nil {
		return nil, fmt.Errorf("resolve config dir %s: %w", p.ConfigDir, err)
	}
	if v.dataDir, err = resolveExisting(p.DataDir); err != nil {
		return nil, fmt.Errorf("resolve data dir %s: %w", p.DataDir, err)
	}
	v.captureDst, v.captureErr = canonicalCaptureDst(p.DataDir, m.JobID)
	// roots.json is read in every mode: R-P3-B1f applies the Root rules to
	// Uninstall too, and there the record is what makes this job's own
	// (by then legitimately non-empty) roots acceptable.
	if v.recorded, err = loadJobRoots(p.DataDir, m.JobID); err != nil {
		return nil, err
	}
	return v, nil
}

// manifestClaimedCwds collects Munge(cwd) for every destination cwd the
// manifest's own CatSession entries claim (see validator.claimedCwds).
func manifestClaimedCwds(m *Manifest, p session.Paths) map[string]bool {
	out := map[string]bool{}
	projects := filepath.Clean(p.ProjectsDir())
	for _, e := range m.Entries {
		if e.Category != session.CatSession {
			continue
		}
		dst := filepath.Clean(e.Dst)
		if !underDir(dst, projects) || dst == projects {
			continue
		}
		rel, err := filepath.Rel(projects, dst)
		if err != nil {
			continue
		}
		first := rel
		if i := strings.IndexRune(rel, filepath.Separator); i >= 0 {
			first = rel[:i]
		}
		out[first] = true
	}
	return out
}

func refusalOf(e Entry, format string, a ...any) *RefusalError {
	return &RefusalError{Refusals: []Refusal{{ID: e.ID, Dst: e.Dst, Category: e.Category, Reason: fmt.Sprintf(format, a...)}}}
}

// check validates one entry against the destination's own reality. It is
// called for every entry before ANY placement happens (Install) and for
// every entry of a manifest being classified (Diff), so the diagnosis at
// preflight and the gate at the moment of the write can never disagree and
// a hostile or stale status map cannot skip a single rule.
//
// The complete set of writes it permits (ruling R-P3-B1e):
//
//   - CatSession under ConfigDir, and never a session.Forbidden path;
//   - CatCapture at exactly job.Dir(DataDir, jobID)/capture.txt, and only
//     as a regular file;
//   - CatRepo/CatWorktree under a Root the manifest declared, where that
//     Root resolves (EvalSymlinks) under $HOME, is not $HOME itself, is
//     outside ConfigDir and DataDir, has no dot-prefixed first component,
//     and — for anything carrying content — was either created by THIS job
//     (jobs/<id>/roots.json) or is not-a-repo mode's user-chosen
//     destination directory;
//   - nothing else: every other category is refused byte-for-byte, no
//     manifest symlink may point outside its own boundary, and every
//     placement re-resolves where it would really land first.
func (v *validator) check(e Entry) error {
	dst := e.Dst
	if !filepath.IsAbs(dst) || dst != filepath.Clean(dst) {
		// A Dst that is not already its own Clean can differ from every
		// path the checks below reason about: "<root>/link/../x" cleans to
		// "<root>/x" while the kernel resolves it through link. Refusing
		// the spelling outright is simpler, and no honest manifest ever
		// contains one (Build joins Clean paths).
		return refusalOf(e, "destination path is not a clean absolute path")
	}
	if !allowedCategory(e.Category) {
		return refusalOf(e, "category is not one of %q/%q/%q/%q", session.CatSession, session.CatCapture, session.CatRepo, session.CatWorktree)
	}
	if !underDir(dst, filepath.Clean(v.p.Home)) {
		return refusalOf(e, "is not under home %s", v.p.Home)
	}
	switch {
	case e.Category == session.CatSession:
		if err := v.checkSession(e, dst); err != nil {
			return err
		}
	case e.Category == session.CatCapture:
		if err := v.checkCapture(e, dst); err != nil {
			return err
		}
	case gitRootCategory(e.Category):
		if err := v.checkGit(e, dst); err != nil {
			return err
		}
	}
	// Where a placement would actually land, resolved (ruling R-P3-B1e
	// item 2b). Diff evaluates it too, so a manifest symlink that would
	// redirect a later write is diagnosed at preflight rather than at the
	// moment of the write.
	return v.checkPlacement(e)
}

func (v *validator) checkSession(e Entry, dst string) error {
	cfg := filepath.Clean(v.p.ConfigDir)
	if !underDir(dst, cfg) {
		return refusalOf(e, "is not under config dir %s", v.p.ConfigDir)
	}
	rel, err := filepath.Rel(cfg, dst)
	if err != nil {
		return refusalOf(e, "relative to config dir: %v", err)
	}
	if session.Forbidden(rel) {
		return refusalOf(e, "is a forbidden path (%s)", rel)
	}
	return v.checkSymlinkTarget(e, dst, cfg, "config dir")
}

func (v *validator) checkCapture(e Entry, dst string) error {
	if v.captureErr != nil {
		return refusalOf(e, "%v", v.captureErr)
	}
	if dst != v.captureDst {
		return refusalOf(e, "is not this job's own capture path (%s)", v.captureDst)
	}
	if !e.IsRegular() {
		return refusalOf(e, "the pane capture is a regular file, not a directory or symlink")
	}
	return nil
}

func (v *validator) checkGit(e Entry, dst string) error {
	if e.Deferred {
		// existing-main's dirty index/worktree files: git-attach applies
		// them itself from staging (with its own DstWorktree/DstMain
		// containment), installManifest keeps them away from Install, and
		// Install refuses any deferred non-capture entry outright. Their
		// Dst is the destination's OWN pre-existing checkout, so no Root
		// rule can or should apply to it.
		return nil
	}
	root, ok := entryRoot(dst, v.roots)
	if !ok {
		return refusalOf(e, "is not under any root this manifest declared")
	}
	reason, err := v.rootReason(root)
	if err != nil {
		return err
	}
	if reason != "" {
		return refusalOf(e, "root %s %s", root.Path, reason)
	}
	resRoot := v.rootRes[root.Path]
	resDst, err := resolveExisting(dst)
	if err != nil {
		return refusalOf(e, "resolve: %v", err)
	}
	if !underDir(resDst, resRoot) {
		return refusalOf(e, "resolves to %s, outside root %s", resDst, resRoot)
	}
	if err := v.checkSymlinkTarget(e, dst, filepath.Clean(root.Path), "root"); err != nil {
		return err
	}
	// Provenance (R-P3-B1e item 3, keyed on the RESOLVED root per B1f N4).
	//
	// A directory entry is "ensure this directory exists" — it writes no
	// content, and existing-main's untracked directories legitimately land
	// inside the destination's own pre-existing checkout — so it never
	// needs the Root to be this job's. Everything that carries CONTENT
	// does. Residual, accepted (B1e/B1f N6): a manifest may therefore
	// create empty directory trees anywhere its declared, rule-passing
	// Roots reach — inside an existing checkout for existing-main, inside
	// a root of its own making otherwise. No content, no overwrite, and
	// Uninstall removes only the directories this job itself created.
	if v.recorded[resRoot] || v.pending[resRoot] {
		return nil
	}
	fi, statErr := os.Lstat(root.Path)
	switch {
	case errors.Is(statErr, os.ErrNotExist):
		// Claimed only if the WHOLE validation pass succeeds (B1f N5) —
		// commitClaims writes roots.json, still before any placement.
		if v.mode == modeInstall {
			v.pending[resRoot] = true
		}
		return nil
	case statErr != nil:
		return refusalOf(e, "root %s: %v", root.Path, statErr)
	case root.MayPreExist:
		// not-a-repo mode's destination cwd. The bit alone is a SOURCE
		// claim, so the destination corroborates it (B1f N2): the root
		// must really be a directory already, and the manifest's own
		// session entries must place this session's transcript under
		// projects/<Munge(root)> — i.e. the session it is transporting
		// says it ran there. Ledgered residual: the destination trusts
		// the session's declared cwd as the driver's choice.
		if !fi.IsDir() {
			return refusalOf(e, "root %s exists and is not a directory", root.Path)
		}
		if !v.claimedCwds[session.Munge(filepath.Clean(root.Path))] && !v.claimedCwds[session.Munge(resRoot)] {
			return refusalOf(e, "root %s may pre-exist only as this session's own destination cwd, and no session file in this manifest claims it", root.Path)
		}
		return nil
	case e.IsDir():
		return nil
	}
	return refusalOf(e, "root %s already exists and was not created by this job", root.Path)
}

// commitClaims records the roots this pass found absent — after every
// entry validated, and still before anything is placed (ruling R-P3-B1f
// N5: a manifest that is refused must leave no claim behind).
func (v *validator) commitClaims() error {
	for root := range v.pending {
		if err := recordJobRoot(v.p.DataDir, v.jobID, v.recorded, root); err != nil {
			return err
		}
		delete(v.pending, root)
	}
	return nil
}

// checkSymlinkTarget is ruling R-P3-B1e item 2a: a manifest symlink may
// only ever point INSIDE the boundary its own entry belongs to. An absolute
// target is refused outright (it names a place on the destination this
// manifest was never granted), and a relative one is resolved against the
// link's own directory and must stay inside boundary.
func (v *validator) checkSymlinkTarget(e Entry, dst, boundary, what string) error {
	if !e.IsSymlink() {
		return nil
	}
	if e.Symlink == "" {
		return refusalOf(e, "symlink entry has no target")
	}
	if filepath.IsAbs(e.Symlink) {
		return refusalOf(e, "symlink target %s is absolute", e.Symlink)
	}
	target := filepath.Join(filepath.Dir(dst), filepath.FromSlash(e.Symlink))
	if !underDir(target, boundary) {
		return refusalOf(e, "symlink target %s escapes the %s %s", e.Symlink, what, boundary)
	}
	res, err := resolveExisting(target)
	if err != nil {
		return refusalOf(e, "resolve symlink target %s: %v", e.Symlink, err)
	}
	resBoundary, err := resolveExisting(boundary)
	if err != nil {
		return refusalOf(e, "resolve %s %s: %v", what, boundary, err)
	}
	if !underDir(res, resBoundary) {
		return refusalOf(e, "symlink target %s resolves to %s, outside the %s %s", e.Symlink, res, what, resBoundary)
	}
	return nil
}

// boundary is the resolved directory an entry's placement must stay inside.
func (v *validator) boundary(e Entry) (string, bool, error) {
	switch {
	case e.Category == session.CatSession:
		return v.configDir, true, nil
	case e.Category == session.CatCapture:
		return v.dataDir, true, nil
	case gitRootCategory(e.Category):
		if e.Deferred || v.mode == modeUninstall {
			return "", false, nil // never placed by this path
		}
		root, ok := entryRoot(filepath.Clean(e.Dst), v.roots)
		if !ok {
			return "", false, refusalOf(e, "is not under any root this manifest declared")
		}
		res, err := v.resolvedRoot(root)
		return res, true, err
	}
	return "", false, nil
}

// checkPlacement re-resolves where a placement would REALLY land — every
// existing component of the path, its parent directories included, through
// filepath.EvalSymlinks — and requires it to still be inside the entry's
// boundary (ruling R-P3-B1e item 2b). Install calls it again immediately
// before every placement, so a symlink an EARLIER entry of the same
// manifest created cannot redirect a later write: by then the link exists,
// and the resolution walks straight out of bounds.
//
// The entry's own Dst is what is resolved, not just its parent: the root
// directory entry of a fresh-main teleport IS the boundary (its parent is
// legitimately outside it), while for everything nested the two are
// equivalent — resolveExisting resolves the whole existing prefix either
// way.
func (v *validator) checkPlacement(e Entry) error {
	b, ok, err := v.boundary(e)
	if err != nil || !ok {
		return err
	}
	dst := filepath.Clean(e.Dst)
	res, err := resolveExisting(dst)
	if err != nil {
		return refusalOf(e, "resolve: %v", err)
	}
	if !underDir(res, b) {
		return refusalOf(e, "resolves to %s, outside %s", res, b)
	}
	return nil
}

// symlinkBoundary is the DECLARED (unresolved) directory a symlink entry's
// target must stay inside — the same one check used in pass 1.
func (v *validator) symlinkBoundary(e Entry) (boundary, what string, ok bool, err error) {
	switch {
	case e.Category == session.CatSession:
		return filepath.Clean(v.p.ConfigDir), "config dir", true, nil
	case gitRootCategory(e.Category) && !e.Deferred:
		root, found := entryRoot(filepath.Clean(e.Dst), v.roots)
		if !found {
			return "", "", false, refusalOf(e, "is not under any root this manifest declared")
		}
		return filepath.Clean(root.Path), "root", true, nil
	}
	return "", "", false, nil
}

// recheckBeforePlacement is the guard Install runs immediately before every
// single placement. It repeats the two checks whose ANSWER CAN CHANGE while
// a manifest is being installed, because an earlier entry of the same
// manifest may have created a symlink since pass 1: where this entry really
// lands (checkPlacement) and, for a symlink entry, where its target really
// points (ruling R-P3-B1f N7).
func (v *validator) recheckBeforePlacement(e Entry) error {
	if err := v.checkPlacement(e); err != nil {
		return err
	}
	boundary, what, ok, err := v.symlinkBoundary(e)
	if err != nil || !ok {
		return err
	}
	return v.checkSymlinkTarget(e, filepath.Clean(e.Dst), boundary, what)
}

// resolvedRoot resolves (and memoizes) one declared root.
func (v *validator) resolvedRoot(root Root) (string, error) {
	if res, ok := v.rootRes[root.Path]; ok {
		return res, nil
	}
	res, err := resolveExisting(root.Path)
	if err != nil {
		return "", fmt.Errorf("resolve root %s: %w", root.Path, err)
	}
	v.rootRes[root.Path] = res
	return res, nil
}

// resolvedPaths holds the destination's own boundaries, resolved once.
type resolvedPaths struct{ home, configDir, dataDir string }

func resolvePaths(p session.Paths) (resolvedPaths, error) {
	var rp resolvedPaths
	var err error
	if rp.home, err = resolveExisting(p.Home); err != nil {
		return rp, fmt.Errorf("resolve home %s: %w", p.Home, err)
	}
	if rp.configDir, err = resolveExisting(p.ConfigDir); err != nil {
		return rp, fmt.Errorf("resolve config dir %s: %w", p.ConfigDir, err)
	}
	if rp.dataDir, err = resolveExisting(p.DataDir); err != nil {
		return rp, fmt.Errorf("resolve data dir %s: %w", p.DataDir, err)
	}
	return rp, nil
}

// rootReason returns "" if the already-resolved res is a legitimate
// boundary for a destination write this manifest (or a git plan) directs
// there, else why it is not — judged on the REAL path (ruling R-P3-B1e
// item 2c).
func (rp resolvedPaths) rootReason(res string) (string, error) {
	switch {
	case !underDir(res, rp.home):
		return fmt.Sprintf("resolves to %s, which is not under home %s", res, rp.home), nil
	case res == rp.home:
		return "is $HOME itself", nil
	case underDir(res, rp.configDir):
		return fmt.Sprintf("resolves to %s, inside the config dir %s", res, rp.configDir), nil
	case underDir(res, rp.dataDir):
		return fmt.Sprintf("resolves to %s, inside the data dir %s", res, rp.dataDir), nil
	}
	dotted, err := firstComponentDotted(rp.home, res)
	if err != nil {
		return "", err
	}
	if dotted {
		return fmt.Sprintf("resolves to %s, whose first path component under home is dot-prefixed", res), nil
	}
	return "", nil
}

// rootReason is the same check for one declared Root, memoized.
func (v *validator) rootReason(root Root) (string, error) {
	if reason, ok := v.rootBad[root.Path]; ok {
		return reason, nil
	}
	res, err := v.resolvedRoot(root)
	if err != nil {
		return "", err
	}
	reason, err := resolvedPaths{home: v.home, configDir: v.configDir, dataDir: v.dataDir}.rootReason(res)
	if err != nil {
		return "", err
	}
	v.rootBad[root.Path] = reason
	return reason, nil
}
