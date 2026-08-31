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
	for _, want := range []string{"laptop.example", "big-storage.example", "preflight", "done", "transfer", "failed", "connection reset", "attempts 2", "l3", "continue " + tsid, "abandon " + tsid} {
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
