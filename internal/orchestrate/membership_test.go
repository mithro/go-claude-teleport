package orchestrate

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/transfer"
)

// A memory file whose content differs on the destination is KEPT, not
// replaced: transfer.Install routes it to MemoryDiffers and drops the
// staged copy. The plan summary counted it with ordinary files and
// announced "diverged and are REPLACED (--force)", which is the opposite
// of what happens -- and is exactly the line that makes a teleport look
// like it is about to destroy the destination's project memory.
//
// Real case (2026-09-15): both ten64 and this desktop have
// projects/-home-tim-github-fpgas-online/memory/MEMORY.md with different
// content, and the dry run claimed it would be replaced.
func TestPlanDoesNotClaimMemoryFilesAreReplaced(t *testing.T) {
	p := &Plan{
		Statuses: map[int]transfer.Status{
			0: transfer.Absent,           // an ordinary file to send
			1: transfer.PresentDifferent, // the memory file
		},
		Extras: &transfer.InstallExtras{
			Memory: []transfer.Entry{{ID: 1, Dst: "/home/alice/.claude/projects/x/memory/MEMORY.md"}},
		},
	}
	var b bytes.Buffer
	p.renderFileSummary(&b)
	out := b.String()

	if strings.Contains(out, "REPLACED") {
		t.Errorf("summary threatens to replace a memory file:\n%s", out)
	}
	if !strings.Contains(out, "memory") {
		t.Errorf("summary says nothing about the diverging memory file:\n%s", out)
	}
	// It is the INDEX, so it is merged, not merely kept -- saying "kept"
	// here would be the same species of wrong as saying "replaced".
	if !strings.Contains(out, "merged") {
		t.Errorf("summary should say the memory index is merged:\n%s", out)
	}
}

// A diverging memory file that is NOT the index really is left alone, and
// the summary must say so rather than promising a merge.
func TestPlanSaysNonIndexMemoryIsKept(t *testing.T) {
	p := &Plan{
		Statuses: map[int]transfer.Status{1: transfer.PresentDifferent},
		Extras: &transfer.InstallExtras{
			Memory: []transfer.Entry{{ID: 1, Dst: "/home/alice/.claude/projects/x/memory/a-note.md"}},
		},
	}
	var b bytes.Buffer
	p.renderFileSummary(&b)
	out := b.String()
	if strings.Contains(out, "REPLACED") || strings.Contains(out, "merged") {
		t.Errorf("a non-index memory file is neither replaced nor merged:\n%s", out)
	}
	if !strings.Contains(out, "KEPT") {
		t.Errorf("summary should say it is kept:\n%s", out)
	}
}

// An ordinary file that diverges is still announced loudly: this must not
// silence the one line that matters for a --force run.
func TestPlanStillWarnsAboutRealReplacements(t *testing.T) {
	p := &Plan{
		Statuses: map[int]transfer.Status{0: transfer.PresentDifferent},
		Extras:   &transfer.InstallExtras{},
	}
	var b bytes.Buffer
	p.renderFileSummary(&b)
	if !strings.Contains(b.String(), "REPLACED") {
		t.Errorf("a diverging ordinary file must still be announced:\n%s", b.String())
	}
}

// No Extras at all (a plan rendered before annotation) must not panic.
func TestPlanFileSummaryWithoutExtras(t *testing.T) {
	p := &Plan{Statuses: map[int]transfer.Status{0: transfer.PresentDifferent}}
	var b bytes.Buffer
	p.renderFileSummary(&b)
	if !strings.Contains(b.String(), "REPLACED") {
		t.Errorf("want the replacement warning:\n%s", b.String())
	}
}
