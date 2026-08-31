package procx

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strconv"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// RegistryForPID reads sessions/<pid>.json and validates procStart against
// startTime; a mismatch (reused pid, stale file) is ok=false, not an error.
func RegistryForPID(sessionsDir string, pid int, startTime string) (*session.Registry, bool, error) {
	path := filepath.Join(sessionsDir, strconv.Itoa(pid)+".json")
	r, err := session.ReadRegistryFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if r.ProcStart == "" || r.ProcStart != startTime {
		return nil, false, nil
	}
	return &r, true, nil
}

// RegistryForSession finds the registry entry naming id. Liveness is NOT
// checked here (the entry may be stale); callers verify with Table.Alive.
func RegistryForSession(sessionsDir string, id session.ID) (*session.Registry, bool, error) {
	regs, err := session.ReadRegistry(sessionsDir)
	if err != nil {
		return nil, false, err
	}
	for i := range regs {
		if regs[i].SessionID == string(id) {
			return &regs[i], true, nil
		}
	}
	return nil, false, nil
}
