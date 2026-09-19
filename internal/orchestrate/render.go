// internal/orchestrate/render.go
package orchestrate

import (
	"fmt"
	"io"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

// Render prints the plan for humans (spec §6 step 1, §8 "every decision
// above is shown"): what --dry-run prints and what precedes confirmation.
func (p *Plan) Render(w io.Writer) {
	s := p.Session
	fmt.Fprintf(w, "Session  %s (%s) on %s\n", s.ID.Short(), s.State, p.SourceInfo.Hostname)
	// The project cwd, not the launch cwd: every decision below this line
	// is made from it, so naming anything else describes a teleport that
	// is not the one about to run.
	fmt.Fprintf(w, "  cwd    %s\n", s.ProjectCwd())
	if s.Branch != "" {
		fmt.Fprintf(w, "  branch %s\n", s.Branch)
	}
	via := ""
	if len(p.Options.Via) > 0 {
		via = " via " + strings.Join(p.Options.Via, ", ")
	}
	fmt.Fprintf(w, "Move     %s %s%s\n", strings.ToUpper(p.Options.Direction[:1])+p.Options.Direction[1:], p.DestInfo.Hostname, via)
	fmt.Fprintf(w, "  claude %s -> %s\n", p.SourceInfo.ClaudeVersion, p.DestInfo.ClaudeVersion)
	if !p.PathMap.Empty() {
		fmt.Fprintln(w, "Paths")
		for _, m := range p.PathMap {
			fmt.Fprintf(w, "  %s -> %s\n", m.From, m.To)
		}
	}
	if p.Git != nil {
		p.renderGit(w)
	}
	p.renderTmux(w)
	p.renderFreezeCaveat(w)
	fmt.Fprintf(w, "End state  %s\n", p.TargetState)
	if len(p.Drift.Diffs) > 0 {
		fmt.Fprintln(w, "Configuration differences")
		p.Drift.Render(w) // claudecfg.Report.Render already redacts secret values
	}
	p.renderFileSummary(w)
}

// renderTmux prints the destination tmux placement. Group and WindowName
// carry tmux's stored (vis-encoded) spelling; UnvisName decodes them here
// for display only — the stored spelling is still what every tmux command
// uses.
func (p *Plan) renderTmux(w io.Writer) {
	fmt.Fprintln(w, "tmux")
	switch {
	case p.Tmux == nil:
		fmt.Fprintln(w, "  none on the destination: Claude is confirmed under a pty and left idle")
	case p.Tmux.CreateSession:
		fmt.Fprintf(w, "  new session %q on %s, window %q in %s\n", tmuxx.UnvisName(p.Tmux.Group), p.Tmux.SocketPath, tmuxx.UnvisName(p.Tmux.WindowName), p.Tmux.Cwd)
	default:
		fmt.Fprintf(w, "  existing group %q on %s, new window %q in %s\n", tmuxx.UnvisName(p.Tmux.Group), p.Tmux.SocketPath, tmuxx.UnvisName(p.Tmux.WindowName), p.Tmux.Cwd)
	}
}

// renderFileSummary counts manifest entries by transfer.Status. Both a
// wholly Absent entry and an FFCandidate (present but stale, needs a
// forward delta) require bytes to be sent, so both count toward "to send";
// FFCandidate is also broken out on its own for visibility.
//
// PresentDifferent is counted too: it is a blocking collision without
// --force (Preflight refuses before this ever renders), so a plan that
// gets here holding one is a --force run about to REPLACE a destination
// file whose content diverged. That is the single most consequential
// thing a plan can say, and it was the one status the summary dropped
// entirely — the entry vanished from every count.
func (p *Plan) renderFileSummary(w io.Writer) {
	// Memory files are never replaced whatever their status: Install routes
	// a diverging one to MemoryDiffers and drops the staged copy, because a
	// project's memory is shared by every session of that project and the
	// destination's copy is as real as the source's. Counting them with
	// ordinary files announced a replacement that cannot happen -- the one
	// line in the whole summary that reads as imminent data loss.
	memory, memIndex := map[int]bool{}, map[int]bool{}
	if p.Extras != nil {
		for _, e := range p.Extras.Memory {
			memory[e.ID] = true
			if session.IsMemoryIndex(e.Dst) {
				memIndex[e.ID] = true
			}
		}
	}
	var toSend, same, ff, staged, replaced, memKept, memMerged int
	for id, st := range p.Statuses {
		if memory[id] {
			switch st {
			case transfer.Absent, transfer.StagedMismatch, transfer.FFCandidate:
				toSend++
			case transfer.PresentDifferent:
				// The index is merged so the memory files copied beside it
				// are findable; every other memory file is left alone.
				if memIndex[id] {
					memMerged++
				} else {
					memKept++
				}
			case transfer.PresentSame:
				same++
			case transfer.StagedSame:
				staged++
			}
			continue
		}
		switch st {
		case transfer.Absent, transfer.StagedMismatch:
			toSend++
		case transfer.FFCandidate:
			toSend++
			ff++
		case transfer.PresentDifferent:
			toSend++
			replaced++
		case transfer.PresentSame:
			same++
		case transfer.StagedSame:
			staged++
		}
	}
	fmt.Fprintf(w, "Files      %d to send, %d already present, %d fast-forward, %d already staged\n", toSend, same, ff, staged)
	if replaced > 0 {
		fmt.Fprintf(w, "  %d destination file(s) diverged and are REPLACED (--force)\n", replaced)
	}
	if memKept > 0 {
		fmt.Fprintf(w, "  %d memory file(s) differ here and are KEPT; the source's copy is not installed\n", memKept)
	}
	if memMerged > 0 {
		fmt.Fprintf(w, "  %d memory index (MEMORY.md) differs here and is merged; existing entries are kept, the source's are added\n", memMerged)
	}
}

// renderFreezeCaveat warns about the one case where the source freeze can
// be undone under us: tmux sends SIGCONT to a stopped pane process of its
// own accord, so a Claude that IS the pane's command (started with no
// shell in between) can be woken mid-teleport. Claude started FROM a shell
// in the pane — the normal case — is unaffected, and the plan cannot tell
// the two apart without inspecting the pane's process tree, so this is
// rendered whenever a running source is being frozen inside tmux.
func (p *Plan) renderFreezeCaveat(w io.Writer) {
	if p.sourceState() != session.StateRunning || p.Session == nil || p.Session.Tmux == nil {
		return
	}
	fmt.Fprintln(w, "  note: if Claude is the pane's OWN command (started without a shell), tmux may SIGCONT it and the freeze will not hold")
}

func (p *Plan) renderGit(w io.Writer) {
	fmt.Fprintln(w, "Git")
	g := p.Git
	switch g.Mode {
	case gitx.ModeNotRepo:
		// No early return: this mode needs its caveats more than any
		// other (renderGitCaveats). Dirty state is empty without a
		// repository, so renderGitDirty prints nothing.
		fmt.Fprintf(w, "  not a repository: %s copied as plain files to %s\n", g.SrcWorktree, g.DstWorktree)
	case gitx.ModeFreshMain:
		fmt.Fprintf(w, "  fresh-main: %s is absent on the destination; the whole repository is transferred\n", g.DstMain)
		if g.Linked {
			fmt.Fprintf(w, "  linked worktree %s is re-attached at %s\n", g.WorktreeName, g.DstWorktree)
		}
	case gitx.ModeExistingMain:
		fmt.Fprintf(w, "  existing-main: %s already exists on the destination (same root commit)\n", g.DstMain)
		switch {
		case g.NeedPack && g.FastForward:
			fmt.Fprintf(w, "  branch %s is fast-forward'ed to %s with a packfile of the missing objects\n", g.Branch, short(g.Tip))
		case g.NeedPack:
			fmt.Fprintf(w, "  branch %s is created at %s from a packfile of the missing objects\n", g.Branch, short(g.Tip))
		default:
			fmt.Fprintf(w, "  branch %s is already at %s; no packfile needed\n", g.Branch, short(g.Tip))
		}
		if g.Linked {
			fmt.Fprintf(w, "  linked worktree is created at %s\n", g.DstWorktree)
		} else {
			fmt.Fprintf(w, "  the main checkout %s is fast-forwarded in place (it must stay clean)\n", g.DstMain)
		}
	}
	dirtyCount := p.renderGitDirty(w)
	p.renderGitCaveats(w, dirtyCount)
}

func (p *Plan) renderGitDirty(w io.Writer) int {
	d := p.Git.Dirty
	n := len(d.Staged) + len(d.Modified) + len(d.Untracked) + len(d.Deleted)
	if n == 0 {
		return 0
	}
	fmt.Fprintf(w, "  dirty state carried: %d staged, %d modified, %d untracked, %d deleted\n", len(d.Staged), len(d.Modified), len(d.Untracked), len(d.Deleted))
	for _, f := range d.Staged {
		fmt.Fprintf(w, "    A %s\n", f)
	}
	for _, f := range d.Modified {
		fmt.Fprintf(w, "    M %s\n", f)
	}
	for _, f := range d.Untracked {
		fmt.Fprintf(w, "    ? %s\n", f)
	}
	for _, f := range d.Deleted {
		fmt.Fprintf(w, "    D %s (stays present on the destination; nothing is ever deleted)\n", f)
	}
	return n
}

// renderGitCaveats surfaces known git-transfer limitations (a ledgered
// controller ruling on this task): each line renders only when its
// condition holds.
//
// gitx.Plan carries no Submodules field — only the source-side gitx.Info
// does, and InventoryGit's result is not persisted into the Plan — so the
// submodule caveat cannot be conditioned on "the repo actually has
// submodules" without adding a new wire field. Per the ruling, it is
// instead rendered unconditionally for every repo plan, alongside the two
// other notes that always apply to repo plans.
func (p *Plan) renderGitCaveats(w io.Writer, dirtyCount int) {
	g := p.Git
	var notes []string
	if g.Mode == gitx.ModeNotRepo {
		// The repo modes are bounded by what git tracks. This one is not
		// bounded by anything: gitx.Files answers ModeNotRepo with a bare
		// walk of SrcWorktree that has no .git skip and no gitignore
		// matcher -- with no repository there is nothing to read ignore
		// rules from -- so --exclude is the only filter that exists.
		notes = append(notes,
			"every file under "+g.SrcWorktree+" travels, including the .git directory of any repository nested inside it",
			"no gitignore is consulted (there is no repository to read one from), so --exclude is the only filter; --exclude '*' sends the session alone and leaves the destination's copy of the directory as it is",
			"an absolute symlink, or one pointing outside the directory, is refused by the destination -- an untracked tree full of virtualenvs and build outputs is where those live, and this check runs before any content is compared",
			"a destination file that already exists with different content refuses the teleport at preflight, before anything is installed; --force does not cover these (it applies only to the session's own files)",
		)
		fmt.Fprintln(w, "  Caveats")
		for _, n := range notes {
			fmt.Fprintf(w, "    - %s\n", n)
		}
		return
	}
	if g.Mode == gitx.ModeExistingMain && dirtyCount > 0 {
		notes = append(notes, "staged deletions do not travel: only staged blobs and dirty working-tree files are sent, so a `git rm --cached` on the source is not replayed on the destination")
	}
	if g.Mode == gitx.ModeExistingMain && p.Options.IncludeIgnored {
		notes = append(notes, "--include-ignored is inert in existing-main mode: gitignored destination content is preserved as-is, and ignored source files are not force-sent")
	}
	notes = append(notes,
		"submodule gitlinks transfer stale: submodule working trees are not synced",
		"the relativeworktrees git extension is unsupported and fails loudly at attach; cleanliness enforced only via .git/info/exclude is not verified",
	)
	fmt.Fprintln(w, "  Caveats")
	for _, n := range notes {
		fmt.Fprintf(w, "    - %s\n", n)
	}
}

func short(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}
