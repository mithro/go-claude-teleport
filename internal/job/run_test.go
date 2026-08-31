package job

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunContinuesAfterCrash(t *testing.T) {
	data := t.TempDir()
	j, err := New(data, sid)
	if err != nil {
		t.Fatal(err)
	}
	var ran []string
	crash := errors.New("simulated crash")
	steps := func(crashAt string) []Step {
		mk := func(name string) Step {
			return Step{
				Name:   name,
				Verify: func(ctx context.Context) (bool, error) { ran = append(ran, "verify:"+name); return false, nil },
				Run: func(ctx context.Context) error {
					ran = append(ran, "run:"+name)
					if name == crashAt {
						return crash
					}
					return nil
				},
			}
		}
		return []Step{mk("one"), mk("two"), mk("three")}
	}

	// first run: steps 1–2 succeed, step 3 "crashes"
	err = Run(context.Background(), j, steps("three"), t.Logf)
	if !errors.Is(err, crash) || !strings.Contains(err.Error(), "three") {
		t.Fatalf("first run err = %v", err)
	}
	if j.Outcome != "failed" || j.Step("three").Status != Failed || j.Step("three").Error != "simulated crash" {
		t.Errorf("journal after crash: %+v", j.Steps)
	}

	// reopen from disk as `continue` would
	j2, ok, err := Open(data, sid)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if name, _ := j2.FirstIncomplete(); name != "three" {
		t.Errorf("FirstIncomplete = %q", name)
	}
	ran = nil
	verifiedDone := map[string]bool{"one": true, "two": true}
	st := steps("")
	for i := range st {
		name := st[i].Name
		st[i].Verify = func(ctx context.Context) (bool, error) {
			ran = append(ran, "verify:"+name)
			return verifiedDone[name], nil
		}
	}
	if err := Run(context.Background(), j2, st, t.Logf); err != nil {
		t.Fatal(err)
	}
	want := "verify:one verify:two verify:three run:three"
	if got := strings.Join(ran, " "); got != want {
		t.Errorf("continue ran %q, want %q (1–2 consulted via Verify, only 3 run)", got, want)
	}
	if !j2.Finished || j2.Outcome != "success" || j2.Step("three").Attempts != 2 {
		t.Errorf("journal after continue: finished=%v outcome=%q attempts=%d", j2.Finished, j2.Outcome, j2.Step("three").Attempts)
	}
	if j2.Step("three").FinishedAt.Before(j2.Step("three").StartedAt) {
		t.Errorf("timestamps: %+v", *j2.Step("three"))
	}
}

func TestRunVerifyErrorAndFailureStops(t *testing.T) {
	data := t.TempDir()
	j, _ := New(data, sid)
	verr := errors.New("precondition gone")
	third := false
	err := Run(context.Background(), j, []Step{
		{Name: "a", Run: func(ctx context.Context) error { return nil }},
		{Name: "b", Verify: func(ctx context.Context) (bool, error) { return false, verr }, Run: func(ctx context.Context) error { return nil }},
		{Name: "c", Run: func(ctx context.Context) error { third = true; return nil }},
	}, t.Logf)
	if !errors.Is(err, verr) {
		t.Fatalf("err = %v", err)
	}
	if third {
		t.Errorf("step c ran after b failed")
	}
	if j.Step("a").Status != Done || j.Step("b").Status != Failed || j.Step("c").Status != Pending {
		t.Errorf("statuses: %+v", j.Steps)
	}
	// state persisted before Run: a Running step is on disk while a step runs
	j3, _ := New(data, "b0b1c2d3-1111-4222-8333-444455556666")
	saw := ""
	Run(context.Background(), j3, []Step{{Name: "x", Run: func(ctx context.Context) error {
		disk, _, _ := Open(data, "b0b1c2d3-1111-4222-8333-444455556666")
		saw = string(disk.Step("x").Status)
		return nil
	}}}, t.Logf)
	if saw != "running" {
		t.Errorf("status on disk during Run = %q, want running", saw)
	}
}

func TestRunNilVerifyTrustsJournal(t *testing.T) {
	data := t.TempDir()
	j, _ := New(data, sid)
	j.Step("a").Status = Done
	ran := false
	if err := Run(context.Background(), j, []Step{{Name: "a", Run: func(ctx context.Context) error { ran = true; return nil }}}, t.Logf); err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Errorf("Done step with nil Verify must be skipped")
	}
}

func TestAppendHistory(t *testing.T) {
	dir := t.TempDir()
	rec := HistoryRecord{At: time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC), SessionID: sid, Direction: "to", From: "laptop.example", To: "big-storage.example", Outcome: "success"}
	if err := AppendHistory(dir, rec); err != nil {
		t.Fatal(err)
	}
	rec.Outcome = "failed"
	if err := AppendHistory(dir, rec); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "history.jsonl"))
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"outcome":"success"`) || !strings.Contains(lines[1], `"to":"big-storage.example"`) {
		t.Errorf("history = %q", raw)
	}
}
