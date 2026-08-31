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
	// the child's session id (field 6 of its stat) equals its own pid: setsid worked
	statLine := out[strings.Index(out, "marker-42\n")+len("marker-42\n"):]
	rest := strings.Fields(statLine[strings.LastIndexByte(statLine, ')')+1:])
	childPID := strings.Fields(statLine)[0]
	if rest[3] != childPID || rest[3] == sessionID(t, os.Getpid()) {
		t.Fatalf("child session=%s pid=%s ours=%s: not detached", rest[3], childPID, sessionID(t, os.Getpid()))
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
