package transfer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestSendReceiveRoundTrip(t *testing.T) {
	m, staging := newTwoHosts(t)
	st, _ := Diff(context.Background(), m, staging, destPaths(t, m))
	need := Need(m, st)

	var buf bytes.Buffer
	var sent []int
	if err := Send(context.Background(), m, need, &buf, func(e Entry, n int64) { sent = append(sent, e.ID) }); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(need, sent); diff != "" {
		t.Errorf("progress ids (-want +got):\n%s", diff)
	}
	var recv []int
	if err := Receive(context.Background(), m, &buf, staging, func(e Entry, n int64) { recv = append(recv, e.ID) }); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(need, recv); diff != "" {
		t.Errorf("received ids (-want +got):\n%s", diff)
	}

	// staged transcript is the REWRITTEN content and matches the manifest hash
	sum, size, err := HashFile(StagedPath(staging, 1))
	if err != nil || sum != m.Entries[1].SHA256 || size != m.Entries[1].Size {
		t.Errorf("staged transcript: sum=%s size=%d err=%v", sum, size, err)
	}
	raw, _ := os.ReadFile(StagedPath(staging, 1))
	if strings.Contains(string(raw), "/home/alice/") {
		t.Errorf("staged transcript still mentions the source home: %s", raw)
	}
	if _, err := os.Stat(StagedPath(staging, 0) + ".dir"); err != nil {
		t.Errorf("dir metadata missing: %v", err)
	}
	link, _ := os.ReadFile(StagedPath(staging, 4) + ".symlink")
	if string(link) != "../"+sid+".jsonl" {
		t.Errorf("symlink metadata = %q", link)
	}
	st, _ = Diff(context.Background(), m, staging, destPaths(t, m))
	for id, s := range st {
		if s != StagedSame {
			t.Errorf("entry %d after receive = %s, want staged-same", id, s)
		}
	}
	entries, _ := os.ReadDir(m.TmpDir)
	if len(entries) != 0 {
		t.Errorf("rewrite temp files left behind: %v", entries)
	}
}

func TestReceiveInterruptedLosesOnlyInFlightEntry(t *testing.T) {
	m, staging := newTwoHosts(t)
	need := Need(m, map[int]Status{0: Absent, 1: Absent, 2: Absent, 3: Absent, 4: Absent})
	var full bytes.Buffer
	if err := Send(context.Background(), m, need, &full, nil); err != nil {
		t.Fatal(err)
	}
	// cut the gzip stream at 60%: some entries complete, one is in flight
	cut := full.Bytes()[:full.Len()*6/10]
	err := Receive(context.Background(), m, bytes.NewReader(cut), staging, nil)
	if err == nil {
		t.Fatal("truncated stream must be an error")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) && !strings.Contains(err.Error(), "unexpected EOF") {
		t.Errorf("err = %v", err)
	}
	parts, _ := filepath.Glob(filepath.Join(staging, "*.part"))
	if len(parts) != 0 {
		t.Errorf(".part remnants: %v", parts)
	}
	st, err := Diff(context.Background(), m, staging, destPaths(t, m))
	if err != nil {
		t.Fatal(err)
	}
	stagedCount := 0
	for _, s := range st {
		if s == StagedSame {
			stagedCount++
		}
	}
	if stagedCount == 0 || stagedCount == len(m.Entries) {
		t.Fatalf("expected a partial staging, got %d/%d staged: %v", stagedCount, len(m.Entries), st)
	}

	// second round: only Need() is sent, and it completes
	rest := Need(m, st)
	var again bytes.Buffer
	if err := Send(context.Background(), m, rest, &again, nil); err != nil {
		t.Fatal(err)
	}
	if err := Receive(context.Background(), m, &again, staging, nil); err != nil {
		t.Fatal(err)
	}
	st, _ = Diff(context.Background(), m, staging, destPaths(t, m))
	for id, s := range st {
		if s != StagedSame {
			t.Errorf("entry %d = %s after resume", id, s)
		}
	}
}

func TestReceiveHashMismatchDeletesPart(t *testing.T) {
	m, staging := newTwoHosts(t)
	var buf bytes.Buffer
	if err := Send(context.Background(), m, []int{3}, &buf, nil); err != nil {
		t.Fatal(err)
	}
	m.Entries[3].SHA256 = sha("something else")
	err := Receive(context.Background(), m, &buf, staging, nil)
	if err == nil || !strings.Contains(err.Error(), "entry 3") {
		t.Fatalf("err = %v, want hash mismatch naming entry 3", err)
	}
	if _, err := os.Stat(StagedPath(staging, 3) + ".part"); !os.IsNotExist(err) {
		t.Errorf(".part must be deleted on mismatch")
	}
	if _, err := os.Stat(StagedPath(staging, 3)); !os.IsNotExist(err) {
		t.Errorf("mismatched entry must not be staged")
	}
}

func TestSendRefusesChangedSource(t *testing.T) {
	m, _ := newTwoHosts(t)
	f, _ := os.OpenFile(m.Entries[3].Src, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("\n{}")
	f.Close()
	err := Send(context.Background(), m, []int{3}, io.Discard, nil)
	if err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("err = %v, want 'changed since manifest was built'", err)
	}
}
