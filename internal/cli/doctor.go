package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

type check struct {
	name, detail string
	ok           bool
}

// localChecks runs the doctor checks that need no remote host.
func (a *app) localChecks() []check {
	var cs []check
	env := a.envSlice()
	pathLookup := func(bin string) (string, error) { return lookInPath(a.env["PATH"], bin) }
	if p, err := pathLookup("claude"); err != nil {
		cs = append(cs, check{"claude on PATH", "not found (install Claude Code and log in)", false})
	} else if v, err := claudeVersionFn(env); err != nil {
		cs = append(cs, check{"claude on PATH", p + " but --version failed: " + err.Error(), false})
	} else {
		cs = append(cs, check{"claude on PATH", p + " (" + v + ")", true})
	}
	if p, err := pathLookup("tmux"); err != nil {
		cs = append(cs, check{"tmux on PATH", "not found (optional: needed to move tmux windows)", true})
	} else {
		cs = append(cs, check{"tmux on PATH", p, true})
	}
	paths, err := a.resolvePaths()
	if err != nil {
		return append(cs, check{"config dir", err.Error(), false})
	}
	if fi, err := os.Stat(paths.ProjectsDir()); err != nil || !fi.IsDir() {
		cs = append(cs, check{"config dir", paths.ConfigDir + " has no projects/ (has Claude ever run here?)", false})
	} else {
		cs = append(cs, check{"config dir", paths.ConfigDir, true})
	}
	if _, err := os.Stat(paths.GlobalJSON); err != nil {
		cs = append(cs, check{"global json", paths.GlobalJSON + " absent (Claude has not run, or a different CLAUDE_CONFIG_DIR)", true})
	} else {
		cs = append(cs, check{"global json", paths.GlobalJSON, true})
	}
	if err := os.MkdirAll(paths.DataDir, 0o700); err != nil {
		cs = append(cs, check{"data dir writable", err.Error(), false})
	} else if f, err := os.CreateTemp(paths.DataDir, ".doctor-*"); err != nil {
		cs = append(cs, check{"data dir writable", err.Error(), false})
	} else {
		f.Close()
		os.Remove(f.Name())
		cs = append(cs, check{"data dir writable", paths.DataDir, true})
	}
	return cs
}

func (a *app) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor [<host>]",
		Short: "check local (and remote) prerequisites",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				// Remote checks arrive with the Plan 02 helper (hello op).
				return Exit(ExitUsage, "doctor %s: remote checks not implemented yet", args[0])
			}
			failed := false
			for _, c := range a.localChecks() {
				status := "ok  "
				if !c.ok {
					status, failed = "FAIL", true
				}
				fmt.Fprintf(a.stdout, "%s  %-18s %s\n", status, c.name, c.detail)
			}
			if failed {
				return Exit(ExitFailed, "doctor found problems")
			}
			return nil
		},
	}
}
