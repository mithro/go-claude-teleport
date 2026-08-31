// internal/orchestrate/faketmux_test.go
package orchestrate

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// fakeTmux implements tmuxx.Transport by interpreting the exact command
// lines tmuxx sends and running each pane as `sh -s` fed through a pipe
// (typed keys go to the pane's stdin, so a running fakeclaude receives
// "/exit" exactly as it would under tmux). capture-pane returns what the
// pane wrote. pane_pid is the sh pid, so procx sees a real process tree.
type fakeTmux struct {
	mu       sync.Mutex
	socket   string
	sessions map[string]string // name -> group
	windows  map[string]*fakeWindow
	panes    map[string]*fakePane
	nextW    int
	nextP    int
	env      func(paneID, sess, win string) []string
}

type fakeWindow struct {
	id, session, name string
	index             int
	autoRename        bool
}

type fakePane struct {
	id, windowID, cwd string
	cmd               *exec.Cmd
	stdin             io.WriteCloser
	out               *lockedBuffer
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := strings.TrimRight(l.b.String(), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func newFakeTmux() *fakeTmux {
	return &fakeTmux{sessions: map[string]string{}, windows: map[string]*fakeWindow{}, panes: map[string]*fakePane{}}
}

func (f *fakeTmux) Close() error { return nil }

// splitArgs tokenises a tmux command line: bare words and "…" words with
// \\, \", \$, \n, \r escapes (the inverse of tmuxx.Quote).
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	inQ, have := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQ && c == '\\' && i+1 < len(s):
			i++
			switch s[i] {
			case 'n':
				cur.WriteByte('\n')
			case 'r':
				cur.WriteByte('\r')
			default:
				cur.WriteByte(s[i])
			}
		case c == '"':
			inQ, have = !inQ, true
		case c == ' ' && !inQ:
			if have || cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteByte(c)
		}
	}
	if have || cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func flag(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func (f *fakeTmux) Run(_ context.Context, cmd string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := splitArgs(cmd)
	if len(a) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	switch a[0] {
	case "list-sessions":
		var out []string
		for name, group := range f.sessions {
			out = append(out, name+"\t"+group)
		}
		return out, nil
	case "new-session":
		sess := flag(a, "-s")
		f.sessions[sess] = ""
		return f.newWindow(sess, flag(a, "-n"), flag(a, "-c"))
	case "new-window":
		target := strings.TrimSuffix(strings.TrimPrefix(flag(a, "-t"), "="), ":")
		if _, ok := f.sessions[target]; !ok {
			return nil, fmt.Errorf("can't find session: %s", target)
		}
		return f.newWindow(target, flag(a, "-n"), flag(a, "-c"))
	case "set-option":
		if w, ok := f.windows[flag(a, "-t")]; ok && a[len(a)-2] == "automatic-rename" {
			w.autoRename = a[len(a)-1] != "off"
			return nil, nil
		}
		return nil, fmt.Errorf("set-option: %q", cmd)
	case "show-options":
		if w, ok := f.windows[flag(a, "-t")]; ok {
			if w.autoRename {
				return []string{"on"}, nil
			}
			return []string{"off"}, nil
		}
		return nil, fmt.Errorf("show-options: no window %q", flag(a, "-t"))
	case "list-panes":
		format := flag(a, "-F")
		target := flag(a, "-t")
		var out []string
		for _, p := range f.panes {
			w := f.windows[p.windowID]
			switch {
			case target == "" && contains(a, "-a"):
				out = append(out, f.describe(w, p, format))
			case target == p.id:
				out = append(out, strconv.Itoa(p.cmd.Process.Pid))
			case strings.HasPrefix(target, "="):
				sw := strings.TrimPrefix(target, "=")
				sess, win, _ := strings.Cut(sw, ":")
				win = strings.TrimPrefix(win, "=")
				if w.session == sess && (win == strconv.Itoa(w.index) || win == w.name) {
					out = append(out, p.id)
				}
			}
		}
		if len(out) == 0 && target != "" {
			return nil, fmt.Errorf("can't find pane: %s", target)
		}
		return out, nil
	case "send-keys":
		p, ok := f.panes[flag(a, "-t")]
		if !ok {
			return nil, fmt.Errorf("can't find pane: %s", flag(a, "-t"))
		}
		var text strings.Builder
		for _, k := range a[3:] {
			if k == "Enter" {
				text.WriteByte('\n')
			} else {
				text.WriteString(k)
			}
		}
		_, err := io.WriteString(p.stdin, text.String())
		return nil, err
	case "capture-pane":
		p, ok := f.panes[flag(a, "-t")]
		if !ok {
			return nil, fmt.Errorf("can't find pane: %s", flag(a, "-t"))
		}
		return p.out.lines(), nil
	case "kill-window":
		w, ok := f.windows[flag(a, "-t")]
		if !ok {
			return nil, fmt.Errorf("can't find window: %s", flag(a, "-t"))
		}
		for id, p := range f.panes {
			if p.windowID == w.id {
				syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
				p.stdin.Close()
				delete(f.panes, id)
			}
		}
		delete(f.windows, w.id)
		return nil, nil
	}
	return nil, fmt.Errorf("fakeTmux: unsupported command %q", cmd)
}

func contains(a []string, s string) bool {
	for _, x := range a {
		if x == s {
			return true
		}
	}
	return false
}

func (f *fakeTmux) newWindow(sess, name, cwd string) ([]string, error) {
	idx := 0
	for _, w := range f.windows {
		if w.session == sess && w.index >= idx {
			idx = w.index + 1
		}
	}
	w := &fakeWindow{id: fmt.Sprintf("@%d", f.nextW), session: sess, name: name, index: idx, autoRename: true}
	f.nextW++
	p := &fakePane{id: fmt.Sprintf("%%%d", f.nextP), windowID: w.id, cwd: cwd, out: &lockedBuffer{}}
	f.nextP++
	cmd := exec.Command("sh", "-s")
	cmd.Dir = cwd
	cmd.Env = f.env(p.id, sess, w.id)
	cmd.Stdout, cmd.Stderr = p.out, p.out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p.cmd, p.stdin = cmd, stdin
	f.windows[w.id] = w
	f.panes[p.id] = p
	return []string{p.id + "\t" + w.id + "\t" + sess}, nil
}

// describe renders the Describe format for one pane (only that format
// is supported: session, group, window id, index, name, pane id, cwd,
// command, pid, history size, title).
func (f *fakeTmux) describe(w *fakeWindow, p *fakePane, format string) string {
	if !strings.HasPrefix(format, "#{session_name}") {
		return ""
	}
	return strings.Join([]string{w.session, f.sessions[w.session], w.id, strconv.Itoa(w.index), w.name, p.id, p.cwd, "sh", strconv.Itoa(p.cmd.Process.Pid), strconv.Itoa(len(p.out.lines())), "title"}, "\t")
}

// killAll ends every pane process (test cleanup).
func (f *fakeTmux) killAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.panes {
		syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		p.stdin.Close()
	}
}
