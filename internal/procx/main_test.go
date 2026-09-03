package procx

import (
	"fmt"
	"os"
	"strconv"
	"testing"
)

// restoreMarker is the stand-in for tmuxx.FreezerRestore in these tests:
// the helper is re-exec'd with a PaneRef whose "socket path" is a file
// path, and the hook writes the pid it was asked to restore there. That
// proves both that Freeze carried the pane ref through argv and that the
// helper ran the hook — without needing a tmux server in a unit test.
//
// Written via a temp file + rename, not a direct os.WriteFile: the test
// that reads this (TestHelperRestoresTheForegroundWhenOwnerDies) polls
// with a plain os.ReadFile and treats ANY successful read as final —
// os.WriteFile's own O_CREATE|O_TRUNC leaves a window where the file
// exists but is still 0 bytes, which that poll can (and, under enough
// concurrent load elsewhere on the machine, did) observe: "restore hook
// ran for pid , want <pid>" is an EMPTY, not a missing, read. os.Rename
// onto path is atomic on the same filesystem (same directory here), so a
// concurrent reader only ever sees the old state (file absent) or the
// complete new one — never a partial write.
func restoreMarker(path string) RestoreFunc {
	return func(pid int) error {
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, []byte(strconv.Itoa(pid)), 0o600); err != nil {
			return err
		}
		return os.Rename(tmp, path)
	}
}

// TestMain turns the test binary into the freezer helper or a "freeze owner"
// when invoked with those argv, so Freeze can re-exec os.Executable().
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "internal-freezer":
			pid, _ := strconv.Atoi(os.Args[2])
			var restore RestoreFunc
			if len(os.Args) > 4 {
				restore = restoreMarker(os.Args[4])
			}
			if err := RunFreezerHelper(pid, os.Args[3], os.NewFile(3, "control"), restore); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		case "freeze-owner":
			// Freeze the target, announce, then hang until killed. The
			// helper must thaw the target when we die.
			pid, _ := strconv.Atoi(os.Args[2])
			self, _ := os.Executable()
			var ref PaneRef
			if len(os.Args) > 4 {
				ref = PaneRef{SocketPath: os.Args[4], PaneID: os.Args[5]}
			}
			if _, err := Freeze(self, pid, os.Args[3], ref); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			fmt.Println("frozen")
			select {}
		}
	}
	os.Exit(m.Run())
}
