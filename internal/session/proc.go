package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ProcStartTime returns field 22 (starttime) of /proc/<pid>/stat as a
// string, the value Claude Code stores as procStart.
func ProcStartTime(procRoot string, pid int) (string, error) {
	path := filepath.Join(procRoot, strconv.Itoa(pid), "stat")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	s := string(data)
	rp := strings.LastIndexByte(s, ')') // comm may contain spaces and parens
	if rp < 0 {
		return "", fmt.Errorf("parse %s: no ')'", path)
	}
	rest := strings.Fields(s[rp+1:]) // rest[0]=state rest[1]=ppid … rest[19]=starttime
	if len(rest) < 20 {
		return "", fmt.Errorf("parse %s: %d fields after comm", path, len(rest))
	}
	return rest[19], nil
}

// ProcAlive reports whether pid exists AND its start time equals procStart.
// An empty procStart never matches (a reused pid must never be trusted).
func ProcAlive(procRoot string, pid int, procStart string) bool {
	if procStart == "" || pid <= 0 {
		return false
	}
	start, err := ProcStartTime(procRoot, pid)
	return err == nil && start == procStart
}
