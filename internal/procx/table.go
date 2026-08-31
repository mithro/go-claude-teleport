// Package procx reads the process table, looks processes up in Claude's
// registry, freezes/thaws a pid safely, waits for exits and spawns
// detached runners.
package procx

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// Proc is one row of the process table.
type Proc struct {
	PID, PPID int
	Comm      string
	Cmdline   []string
	StartTime string // field 22 of /proc/<pid>/stat, as a string
}

// Table is a snapshot of /proc.
type Table struct {
	byPID    map[int]Proc
	children map[int][]int
}

// StartTime is session.ProcStartTime (re-exported so callers need only procx).
func StartTime(procRoot string, pid int) (string, error) { return session.ProcStartTime(procRoot, pid) }

// Scan reads every numeric directory under procRoot ("/proc" in production).
func Scan(procRoot string) (*Table, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", procRoot, err)
	}
	t := &Table{byPID: map[int]Proc{}, children: map[int][]int{}}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "stat"))
		if err != nil {
			continue // process exited mid-scan
		}
		p, ok := parseStat(pid, stat)
		if !ok {
			continue
		}
		if cl, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "cmdline")); err == nil && len(cl) > 0 {
			for _, part := range bytes.Split(bytes.TrimRight(cl, "\x00"), []byte{0}) {
				p.Cmdline = append(p.Cmdline, string(part))
			}
		}
		t.byPID[pid] = p
		t.children[p.PPID] = append(t.children[p.PPID], pid)
	}
	for ppid := range t.children {
		sort.Ints(t.children[ppid])
	}
	return t, nil
}

// parseStat handles comm containing spaces/parens by splitting on the LAST ')'.
func parseStat(pid int, stat []byte) (Proc, bool) {
	s := string(stat)
	lp, rp := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if lp < 0 || rp < 0 || rp < lp {
		return Proc{}, false
	}
	rest := strings.Fields(s[rp+1:]) // rest[0]=state rest[1]=ppid … rest[19]=starttime
	if len(rest) < 20 {
		return Proc{}, false
	}
	ppid, err := strconv.Atoi(rest[1])
	if err != nil {
		return Proc{}, false
	}
	return Proc{PID: pid, PPID: ppid, Comm: s[lp+1 : rp], StartTime: rest[19]}, true
}

func (t *Table) Get(pid int) (Proc, bool) { p, ok := t.byPID[pid]; return p, ok }

// Children returns the direct children of pid, ascending.
func (t *Table) Children(pid int) []int { return append([]int(nil), t.children[pid]...) }

// Subtree returns pid and all descendants, breadth-first.
func (t *Table) Subtree(pid int) []int {
	out := []int{pid}
	for i := 0; i < len(out); i++ {
		out = append(out, t.children[out[i]]...)
	}
	return out
}

// Alive reports whether pid exists with exactly startTime. An empty
// startTime never matches: a reused pid must never be trusted.
func (t *Table) Alive(pid int, startTime string) bool {
	p, ok := t.byPID[pid]
	return ok && startTime != "" && p.StartTime == startTime
}
