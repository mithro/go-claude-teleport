// Package placeholder implements `claude-teleport placeholder`: the command
// typed into a pane instead of relaunching Claude blindly. It shows WHICH
// conversation the pane held and waits for Enter (spec §11). Ported from
// go-tmux-saver's internal/resume (Apache-2.0, same author).
package placeholder

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// Options configures one placeholder run.
type Options struct {
	SessionID    string
	SavedOutput  string // file to print above the banner ("" = none)
	Now          bool   // skip the confirm wait
	TeleportedTo string
	TeleportedAt string // ISO 8601
	ProjectsDir  string
	Home         string
}

// Decision is what the placeholder resolved to: exec Argv (from Chdir when
// non-empty), or Skip back to the pane's shell.
type Decision struct {
	Argv  []string // claude --resume <sid>   (or claude)
	Chdir string
	Skip  bool
}

func shortenHome(home, p string) string {
	if home != "" && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// ChdirTarget returns the directory to resume from, or "". `claude --resume`
// is project-scoped: it resolves the id against the project matching the
// CURRENT directory's munged name, so the pane must cd back to the launch
// cwd (when it still exists and really is the transcript's project).
func ChdirTarget(m session.Meta, transcript string) string {
	cwd := m.LaunchCwd
	if cwd == "" || transcript == "" {
		return ""
	}
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		return ""
	}
	if session.Munge(cwd) != filepath.Base(filepath.Dir(transcript)) {
		return ""
	}
	return cwd
}

// Render writes the banner. tty enables ANSI styling.
func Render(w io.Writer, o Options, meta *session.Meta, tty bool) {
	b, d, c, y, r := "", "", "", "", ""
	if tty {
		b, d, c, y, r = "\033[1m", "\033[2m", "\033[36m", "\033[33m", "\033[0m"
	}
	sid := o.SessionID
	if sid == "" {
		fmt.Fprintf(w, "\n%sResume Claude%s %s(no session id — picker)%s\n", b, r, d, r)
		fmt.Fprintf(w, "%s  Enter = open the resume picker · Ctrl-C = shell%s\n\n", d, r)
		return
	}
	fmt.Fprintf(w, "\n%sResume Claude session%s  %s%s%s%s…%s\n", b, r, c, sid[:8], r, d, r)
	if meta != nil {
		loc := shortenHome(o.Home, meta.LaunchCwd)
		if meta.Branch != "" {
			if loc != "" {
				loc = fmt.Sprintf("%s  %s@ %s%s", loc, d, meta.Branch, r)
			} else {
				loc = meta.Branch
			}
		}
		if loc != "" {
			fmt.Fprintf(w, "  %s\n", loc)
		}
		if meta.WorkCwd != "" && meta.WorkCwd != meta.LaunchCwd {
			fmt.Fprintf(w, "  %s↳ worktree %s%s\n", d, shortenHome(o.Home, meta.WorkCwd), r)
		}
		fmt.Fprintf(w, "  %s\"%s%s%s\"%s\n", d, r, meta.Label(), d, r)
		if meta.LastTS != "" {
			fmt.Fprintf(w, "  %slast active %s%s\n", d, meta.LastTS, r)
		}
	} else {
		fmt.Fprintf(w, "  %s(transcript not found — will still try to resume)%s\n", d, r)
	}
	if o.TeleportedTo != "" {
		at := o.TeleportedAt
		if at == "" {
			at = "an unknown time"
		}
		fmt.Fprintf(w, "  %s⚠ teleported to %s at %s — resuming here forks the session%s\n", y, o.TeleportedTo, at, r)
	}
	if o.Now {
		fmt.Fprintf(w, "%s  resuming now%s\n\n", d, r)
	} else {
		fmt.Fprintf(w, "%s  Enter = resume · Ctrl-C = shell%s\n\n", d, r)
	}
}

// Decide runs the whole placeholder flow against injected I/O: print the
// saved output, render the banner, wait for Enter (unless Now, or stdin is
// not a tty — a send-keys restore has no human to wait on), announce the
// choice, and return what to exec. readLine returning an error (Ctrl-C /
// Ctrl-D) means skip. A visible line always records the choice.
func Decide(w io.Writer, o Options, stdoutTTY, stdinTTY bool, readLine func() (string, error)) Decision {
	if o.SavedOutput != "" {
		if data, err := os.ReadFile(o.SavedOutput); err == nil {
			w.Write(data)
		} else {
			fmt.Fprintf(w, "(saved output %s not readable: %v)\n", o.SavedOutput, err)
		}
	}
	sid := strings.ToLower(strings.TrimSpace(o.SessionID))
	var meta *session.Meta
	transcript := ""
	argv := []string{"claude"}
	if session.IsUUID(sid) {
		if tp, err := session.FindTranscript(o.ProjectsDir, session.ID(sid)); err == nil {
			transcript = tp
			if m, err := session.ReadMeta(tp); err == nil {
				meta = &m
			}
		}
		argv = []string{"claude", "--resume", sid}
	} else {
		sid = "" // junk that isn't a uuid → plain claude (resume picker)
	}
	o.SessionID = sid
	Render(w, o, meta, stdoutTTY)

	d, grn, r := "", "", ""
	if stdoutTTY {
		d, grn, r = "\033[2m", "\033[32m", "\033[0m"
	}
	if stdinTTY && !o.Now {
		if _, err := readLine(); err != nil {
			fmt.Fprintf(w, "%s↩ skipped — shell ready%s\n", d, r)
			return Decision{Skip: true}
		}
	}
	if sid != "" {
		fmt.Fprintf(w, "%s↳ resuming%s %s%s…%s\n", grn, r, d, sid[:8], r)
	} else {
		fmt.Fprintf(w, "%s↳ opening resume picker%s%s…%s\n", grn, r, d, r)
	}
	chdir := ""
	if meta != nil {
		chdir = ChdirTarget(*meta, transcript)
		if chdir == "" && meta.LaunchCwd != "" {
			d, r := "", ""
			if stdoutTTY {
				d, r = "\033[2m", "\033[0m"
			}
			fmt.Fprintf(w, "%s(launch directory not usable — resuming from the current directory)%s\n", d, r)
		}
	}
	return Decision{Argv: argv, Chdir: chdir}
}
