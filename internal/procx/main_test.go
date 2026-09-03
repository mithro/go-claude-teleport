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
func restoreMarker(path string) RestoreFunc {
	return func(pid int) error {
		return os.WriteFile(path, []byte(strconv.Itoa(pid)), 0o600)
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
