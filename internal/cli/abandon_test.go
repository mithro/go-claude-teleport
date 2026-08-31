package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

func TestAbandonMarksAndRemovesStaging(t *testing.T) {
	env, home := testEnv(t)
	dataDir := filepath.Join(home, ".local", "share", "claude-teleport")
	j, _ := job.New(dataDir, tsid)
	j.Direction = "to"
	j.Save()
	staging := job.StagingDir(dataDir, tsid)
	os.MkdirAll(staging, 0o700)
	os.WriteFile(filepath.Join(staging, "1"), []byte("x"), 0o600)

	var out, errOut bytes.Buffer
	if code := Main([]string{"abandon", tsid}, strings.NewReader(""), &out, &errOut, env); code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	got, _, _ := job.Open(dataDir, tsid)
	if got.Outcome != "abandoned" || !got.Finished {
		t.Errorf("journal = %+v", got)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Errorf("staging dir must be removed")
	}
	if _, err := os.Stat(j.LogPath()); err == nil {
		// log may not exist; only assert the job dir survives
	}
	if _, err := os.Stat(j.Dir); err != nil {
		t.Errorf("job dir must survive abandon: %v", err)
	}

	// source side cannot delete destination files in this plan
	if code := Main([]string{"abandon", tsid, "--delete-destination-files"}, strings.NewReader(""), &out, &errOut, env); code != ExitFailed || !strings.Contains(errOut.String(), "Plan 03") {
		t.Errorf("source-side delete: exit %d stderr %q", code, errOut.String())
	}
}

func TestAbandonDeleteDestinationFilesLocally(t *testing.T) {
	env, home := testEnv(t)
	dataDir := filepath.Join(home, ".local", "share", "claude-teleport")
	cfg := filepath.Join(home, ".claude")
	j, _ := job.New(dataDir, tsid)
	j.Direction = "from" // this host is the destination
	j.Save()
	installed := filepath.Join(cfg, "projects", "-home-bob-work", tsid+".jsonl")
	os.MkdirAll(filepath.Dir(installed), 0o700)
	os.WriteFile(installed, []byte("{}\n"), 0o600)
	modified := filepath.Join(cfg, "todos", tsid+".json")
	os.MkdirAll(filepath.Dir(modified), 0o700)
	os.WriteFile(modified, []byte("changed"), 0o600)
	m := &transfer.Manifest{Version: 1, JobID: tsid, SessionID: tsid, Entries: []transfer.Entry{
		{ID: 0, Category: session.CatSession, Dst: filepath.Dir(installed), Mode: uint32(os.ModeDir | 0o700)},
		{ID: 1, Category: session.CatSession, Dst: installed, Size: 3, Mode: 0o600, SHA256: shaOf("{}\n")},
		{ID: 2, Category: session.CatSession, Dst: modified, Size: 2, Mode: 0o600, SHA256: shaOf("{}")},
	}}
	m.Save(j.ManifestPath())

	var out, errOut bytes.Buffer
	if code := Main([]string{"abandon", tsid, "--delete-destination-files"}, strings.NewReader(""), &out, &errOut, env); code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if _, err := os.Stat(installed); !os.IsNotExist(err) {
		t.Errorf("matching installed file should be removed")
	}
	if _, err := os.Stat(modified); err != nil {
		t.Errorf("modified file must be kept: %v", err)
	}
	if !strings.Contains(out.String(), installed) {
		t.Errorf("removed paths must be printed: %s", out.String())
	}
}

func shaOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
