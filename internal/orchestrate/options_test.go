// internal/orchestrate/options_test.go
package orchestrate

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

const sid = "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c"

func TestPlaceholderArgv(t *testing.T) {
	got := PlaceholderArgv(session.ID(sid), "/home/bob/.local/share/claude-teleport/jobs/"+sid+"/capture.txt", true, "", "")
	want := []string{"claude-teleport", "placeholder", "--resume", sid, "--saved-output", "/home/bob/.local/share/claude-teleport/jobs/" + sid + "/capture.txt", "--now"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	got = PlaceholderArgv(session.ID(sid), "", false, "big-storage.example", "2026-08-27T10:00:00Z")
	want = []string{"claude-teleport", "placeholder", "--resume", sid, "--teleported-to", "big-storage.example", "--teleported-at", "2026-08-27T10:00:00Z"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

func TestSuspendArgvPrefersClaudeResume(t *testing.T) {
	if got := SuspendArgv(session.ID(sid), "/c.txt", true); got[0] != "claude-resume" || got[1] != sid || got[3] != "/c.txt" {
		t.Errorf("claude-resume argv = %v", got)
	}
	if got := SuspendArgv(session.ID(sid), "/c.txt", false); got[0] != "claude-teleport" || got[1] != "placeholder" || got[len(got)-1] == "--now" {
		t.Errorf("placeholder argv = %v", got)
	}
}

func TestPlanRoundTripsThroughJournal(t *testing.T) {
	p := &Plan{JobID: sid, TargetState: "running", Statuses: map[int]transfer.Status{}}
	p.Options.Direction = "to"
	p.Options.Target = "alice@big-storage.example"
	p.Options.Via = []string{"jump.example"}
	raw, err := p.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	var probe map[string]json.RawMessage
	json.Unmarshal(raw, &probe)
	for _, key := range []string{"options", "statuses", "git", "extras", "target_state"} {
		if _, ok := probe[key]; !ok {
			t.Errorf("plan JSON lacks %q (remote.planView depends on it)", key)
		}
	}
	j := &job.Journal{ID: sid, Plan: raw}
	got, err := PlanFromJournal(j)
	if err != nil {
		t.Fatal(err)
	}
	if got.Options.Target != "alice@big-storage.example" || got.Options.Via[0] != "jump.example" || got.TargetState != "running" {
		t.Errorf("round trip = %+v", got.Options)
	}
}

func TestErrorTypes(t *testing.T) {
	var re *RefusedError
	if !errors.As(refusef("x %d", 1), &re) || re.Error() != "refused: x 1" {
		t.Error("RefusedError")
	}
	var ue *UnreachableError
	if !errors.As(&UnreachableError{Host: "big-storage.example", Err: errors.New("dial")}, &ue) {
		t.Error("UnreachableError")
	}
}
