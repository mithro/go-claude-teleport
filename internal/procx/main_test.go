package procx

import (
	"fmt"
	"os"
	"strconv"
	"testing"
)

// TestMain turns the test binary into the freezer helper or a "freeze owner"
// when invoked with those argv, so Freeze can re-exec os.Executable().
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "internal-freezer":
			pid, _ := strconv.Atoi(os.Args[2])
			if err := RunFreezerHelper(pid, os.Args[3], os.NewFile(3, "control")); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		case "freeze-owner":
			// Freeze the target, announce, then hang until killed. The
			// helper must thaw the target when we die.
			pid, _ := strconv.Atoi(os.Args[2])
			self, _ := os.Executable()
			if _, err := Freeze(self, pid, os.Args[3]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			fmt.Println("frozen")
			select {}
		}
	}
	os.Exit(m.Run())
}
