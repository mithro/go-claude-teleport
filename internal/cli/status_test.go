package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

func TestStatusRendersJournal(t *testing.T) {
	env, home := testEnv(t)
	dataDir := filepath.Join(home, ".local", "share", "claude-teleport")
	j, _ := job.New(dataDir, tsid)
	j.Direction, j.SourceHost, j.DestHost = "to", "laptop.example", "big-storage.example"
	j.Step("preflight").Status = job.Done
	s := j.Step("transfer")
	s.Status, s.Error, s.Attempts = job.Failed, "connection reset", 2
	j.Outcome = "failed"
	j.Save()
	os.WriteFile(j.LogPath(), []byte("l1\nl2\nl3\n"), 0o600)

	var out, errOut bytes.Buffer
	code := Main([]string{"status", tsid}, strings.NewReader(""), &out, &errOut, env)
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	for _, want := range []string{"laptop.example", "big-storage.example", "preflight", "done", "transfer", "failed", "connection reset", "attempts 2", "l3", "status " + tsid, "continue " + tsid, "abandon " + tsid} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status output lacks %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	code = Main([]string{"status", tsid, "--json"}, strings.NewReader(""), &out, &errOut, env)
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var doc struct {
		Journal job.Journal `json:"journal"`
		LogTail []string    `json:"log_tail"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("json: %v\n%s", err, out.String())
	}
	if doc.Journal.Outcome != "failed" || len(doc.LogTail) != 3 {
		t.Errorf("json doc = %+v", doc)
	}
	_ = time.Now
}

func TestStatusRendersManifestSummary(t *testing.T) {
	env, home := testEnv(t)
	dataDir := filepath.Join(home, ".local", "share", "claude-teleport")
	j, _ := job.New(dataDir, tsid)
	j.Direction, j.SourceHost, j.DestHost = "to", "laptop.example", "big-storage.example"
	j.Save()
	m := &transfer.Manifest{Version: 1, JobID: tsid, SessionID: tsid, Entries: []transfer.Entry{
		{ID: 0, Category: session.CatSession, Dst: "/home/alice/.claude/todos/" + tsid + ".json", Size: 100},
		{ID: 1, Category: session.CatSession, Dst: "/home/alice/.claude/projects/-home-alice-work/" + tsid + ".jsonl", Size: 250},
	}, Skipped: []session.Skipped{{Path: "/home/alice/.claude/ide/locks/1234.lock", Reason: "forbidden"}}}
	if err := m.Save(j.ManifestPath()); err != nil {
		t.Fatalf("save manifest: %v", err)
	}

	var out, errOut bytes.Buffer
	code := Main([]string{"status", tsid}, strings.NewReader(""), &out, &errOut, env)
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "manifest: 2 entries, 350 bytes, 1 skipped") {
		t.Errorf("status output lacks manifest summary line:\n%s", out.String())
	}

	out.Reset()
	code = Main([]string{"status", tsid, "--json"}, strings.NewReader(""), &out, &errOut, env)
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("json: %v\n%s", err, out.String())
	}
	manifest, ok := doc["manifest"]
	if !ok || string(manifest) == "null" {
		t.Errorf("json doc's \"manifest\" key must be present and non-null, got %q", manifest)
	}
}

func TestStatusMissingAndBadID(t *testing.T) {
	env, _ := testEnv(t)
	var out, errOut bytes.Buffer
	if code := Main([]string{"status", tsid}, strings.NewReader(""), &out, &errOut, env); code != ExitFailed || !strings.Contains(errOut.String(), "no job") {
		t.Errorf("missing: exit %d stderr %q", code, errOut.String())
	}
	if code := Main([]string{"status", "not-a-uuid"}, strings.NewReader(""), &out, &errOut, env); code != ExitUsage {
		t.Errorf("bad id: exit %d", code)
	}
}

// TestStatusHintsAfterAFailedJob pins A6: the runner marks a FAILED job
// finished as well, so gating the next-step hint on Finished hid
// continue/abandon from exactly the reader who needs them — while a
// successful or abandoned job (nothing left to do) must still not show
// them.
func TestStatusHintsAfterAFailedJob(t *testing.T) {
	for _, tc := range []struct {
		outcome string
		want    bool
	}{
		{"failed", true},
		{"", true},
		{"success", false},
		{"abandoned", false},
	} {
		env, home := testEnv(t)
		dataDir := filepath.Join(home, ".local", "share", "claude-teleport")
		j, _ := job.New(dataDir, tsid)
		j.Direction, j.SourceHost, j.DestHost = "to", "laptop.example", "big-storage.example"
		st := j.Step("transfer")
		st.Status, st.Error = job.Failed, "tar stream: EOF"
		j.Outcome, j.Finished = tc.outcome, tc.outcome != ""
		if err := j.Save(); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := run(t, env, "status", tsid)
		if code != ExitOK {
			t.Fatalf("status: %d %s", code, stderr)
		}
		if got := strings.Contains(out, "continue "+tsid); got != tc.want {
			t.Errorf("outcome %q: next-step hint shown = %v, want %v:\n%s", tc.outcome, got, tc.want, out)
		}
	}
}
