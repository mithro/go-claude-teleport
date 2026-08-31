package procx

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sessionID reads field 6 (session id) of /proc/<pid>/stat.
func sessionID(t *testing.T, pid int) string {
	t.Helper()
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	rest := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
	return rest[3] // state ppid pgrp session
}

func TestSpawnDetached(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log.txt")
	pid, err := SpawnDetached([]string{"sh", "-c", "echo hello; echo \"$MARK\"; cat /proc/self/stat"}, dir, log, []string{"PATH=" + os.Getenv("PATH"), "MARK=marker-42"})
	if err != nil {
		t.Fatal(err)
	}
	if pid <= 0 {
		t.Fatalf("pid = %d", pid)
	}
	var out string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(log)
		out = string(b)
		if strings.Contains(out, "marker-42") && strings.Contains(out, ")") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.HasPrefix(out, "hello\nmarker-42\n") {
		t.Fatalf("log = %q", out)
	}
	// the child's session id (field 6 of its stat) must differ from the parent's.
	// We do NOT assert session==pid (leadership) because sandboxes/wrappers may
	// interpose a parent process that becomes the session leader instead.
	// The spec (§6) requires only that the runner not remain in the caller's session.
	statLine := out[strings.Index(out, "marker-42\n")+len("marker-42\n"):]
	rest := strings.Fields(statLine[strings.LastIndexByte(statLine, ')')+1:])
	childSessionID := rest[3]
	if childSessionID == sessionID(t, os.Getpid()) {
		t.Fatalf("child still in caller's session: %s", childSessionID)
	}
	if fi, _ := os.Stat(log); fi.Mode().Perm() != 0o600 {
		t.Fatalf("log mode %v", fi.Mode())
	}
}

func TestSpawnDetachedMissingBinary(t *testing.T) {
	if _, err := SpawnDetached([]string{"/nonexistent/binary"}, t.TempDir(), filepath.Join(t.TempDir(), "l"), nil); err == nil {
		t.Fatal("expected error")
	}
}
