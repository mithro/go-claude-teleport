package remote

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/job"
)

// argsPrototypes names the args type of EVERY dispatched op (nil for the
// ops that take no args). It exists so the two tests below can enumerate
// what the wire accepts: nothing in production reads it, and
// TestEveryDispatchedOpDeclaresItsArgsType fails the moment a new op is
// added without listing it here — which is exactly when its author must
// decide whether the new op carries a job id (R-P3-23n).
var argsPrototypes = map[string]any{
	OpHello:            HelloArgs{},
	OpPaths:            nil,
	OpResolveSession:   ResolveSessionArgs{},
	OpInventorySession: InventorySessionArgs{},
	OpInventoryHost:    InventoryHostArgs{},
	OpInventoryGit:     InventoryGitArgs{},
	OpGitDestState:     GitDestStateArgs{},
	OpInventoryTmux:    InventoryTmuxArgs{},
	OpManifestDiff:     ManifestDiffArgs{},
	OpInstallExtras:    InstallExtrasArgs{},
	OpInstall:          InstallArgs{},
	OpGitAttach:        GitAttachArgs{},
	OpFreeze:           FreezeArgs{},
	OpThaw:             ThawArgs{},
	OpCapture:          CaptureArgs{},
	OpOpenWindow:       OpenWindowArgs{},
	OpStartClaude:      StartClaudeArgs{},
	OpConfirmClaude:    ConfirmClaudeArgs{},
	OpExitClaude:       ExitClaudeArgs{},
	OpTypeCommand:      TypeCommandArgs{},
	OpPaneState:        PaneStateArgs{},
	OpRunPtyResume:     RunPtyResumeArgs{},
	OpJournalGet:       JournalGetArgs{},
	OpJournalPut:       JournalPutArgs{},
	OpRecord:           RecordArgs{},
	OpGitFiles:         gitFilesArgs{},
	OpGitSourceFacts:   gitSourceFactsArgs{},
	OpTmuxSessions:     tmuxSessionsArgs{},
	OpKillWindow:       killWindowArgs{},
	OpClaudeStatus:     claudeStatusArgs{},
	OpBuildManifest:    buildManifestArgs{},
	OpSessionExtras:    sessionExtrasArgs{},
	OpCleanup:          cleanupArgs{},
	OpListSessions:     nil,
	OpDeleteInstalled:  deleteInstalledArgs{},
	OpRemoveJob:        removeJobArgs{},
}

func TestEveryDispatchedOpDeclaresItsArgsType(t *testing.T) {
	for op := range dispatch {
		if _, ok := argsPrototypes[op]; !ok {
			t.Errorf("op %q is dispatched but has no entry in argsPrototypes: add one, and if its args carry a job id give the args type a wireJobIDs method (R-P3-23n)", op)
		}
	}
	for op := range argsPrototypes {
		if _, ok := dispatch[op]; !ok {
			t.Errorf("argsPrototypes lists %q, which is not dispatched", op)
		}
	}
}

// TestEveryArgsTypeWithAJobIDIsValidatedAtDispatch is ruling R-P3-23n's
// "a future op cannot forget it" clause: decode (the one funnel every
// handler's args pass through) validates any args value implementing
// jobIDCarrier, so an args type with a job-id field that does NOT
// implement it would slip an unvalidated id through to a handler.
// Only top-level fields are checked here, and deliberately so. The wire's
// path-forming job id is always a top-level job_id arg; a job id nested
// inside a payload (transfer.Manifest.JobID, job.Journal.ID reached
// through a pointer) names no directory by itself, so the defence for
// those is Local's own checkJobID before it touches the disk — see
// TestLocalRejectsHostileJobIDs — not this reflection walk. JournalPutArgs
// is the exception that proves it: its id IS path-forming, reflection
// cannot see it through the pointer, and it therefore gets an explicit
// test of its own below.
func TestEveryArgsTypeWithAJobIDIsValidatedAtDispatch(t *testing.T) {
	for op, proto := range argsPrototypes {
		if proto == nil {
			continue
		}
		rt := reflect.TypeOf(proto)
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			// The json tag may carry options ("job_id,omitempty"): compare
			// the NAME only, or a future omitempty would quietly take a
			// field out of this check.
			tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if f.Type.Kind() != reflect.String || (f.Name != "JobID" && tag != "job_id") {
				continue
			}
			c, ok := proto.(jobIDCarrier)
			if !ok {
				t.Errorf("op %q: args type %T has field %s but does not implement jobIDCarrier, so decode cannot validate it", op, proto, f.Name)
				continue
			}
			// The method must actually report THAT field, not a
			// forgotten copy of some other one.
			v := reflect.New(rt).Elem()
			v.Field(i).SetString("sentinel-id")
			got := v.Interface().(jobIDCarrier).wireJobIDs()
			if len(got) != 1 || got[0] != "sentinel-id" {
				t.Errorf("op %q: %T.wireJobIDs() = %q, want [sentinel-id] (the value of %s)", op, proto, got, f.Name)
			}
			_ = c
		}
	}
}

// TestJobIDOpsTableCoversEveryCarrier ties the hostile-id table test to
// the enumeration above: every args type decode validates must also be
// exercised end to end over Serve, and nothing may be listed there that
// is not really a carrier.
func TestJobIDOpsTableCoversEveryCarrier(t *testing.T) {
	tested := map[string]bool{}
	for _, o := range jobIDOps() {
		tested[o.op] = true
	}
	for op, proto := range argsPrototypes {
		if proto == nil {
			continue
		}
		_, carrier := proto.(jobIDCarrier)
		if carrier && !tested[op] {
			t.Errorf("op %q carries a job id but TestServeRejectsHostileJobIDs does not exercise it", op)
		}
		if !carrier && tested[op] {
			t.Errorf("op %q is exercised as a job-id op but its args type %T is not a jobIDCarrier", op, proto)
		}
	}
}

// TestJournalPutArgsCarriesTheJournalsID is the one carrier reflection
// cannot spot: job-journal-put's id is the journal's own ID field, and
// that is what JournalPut joins into jobs/<id>.
func TestJournalPutArgsCarriesTheJournalsID(t *testing.T) {
	a := JournalPutArgs{Journal: &job.Journal{ID: "sentinel-id"}}
	c, ok := any(a).(jobIDCarrier)
	if !ok {
		t.Fatal("JournalPutArgs must implement jobIDCarrier")
	}
	if got := c.wireJobIDs(); len(got) != 1 || got[0] != "sentinel-id" {
		t.Errorf("wireJobIDs() = %q, want [sentinel-id]", got)
	}
	// A nil journal carries no id to validate; the handler rejects it.
	if got := any(JournalPutArgs{}).(jobIDCarrier).wireJobIDs(); len(got) != 0 {
		t.Errorf("nil journal: wireJobIDs() = %q, want none", got)
	}
}
