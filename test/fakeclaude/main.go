// Command fakeclaude reproduces the observable on-disk behaviour of Claude
// Code 2.1.247 (spec §12): registry file, transcript records, history,
// --resume/--session-id/-p/--version, /exit, signals, and a "! bash" child.
// It is built by tests and put on PATH as `claude`. It never talks to any
// network.
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/session"
)

const defaultVersion = "2.1.247"

type fake struct {
	version, cfg, cwd, branch, sid, transcript, registry string
	pid                                                  int
	procStart, tmux                                      string
	startedAt                                            int64
	lastUUID                                             string
}

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func uuid4() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func now() string  { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }
func nowMS() int64 { return time.Now().UnixMilli() }
func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	f := &fake{version: getenv("FAKECLAUDE_VERSION", defaultVersion), branch: getenv("FAKECLAUDE_BRANCH", "main"), pid: os.Getpid()}
	var resume, sessionID, prompt string
	printMode := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--version", "-v":
			fmt.Fprintf(stdout, "%s (Claude Code)\n", f.version)
			return 0
		case "--resume", "-r":
			if i+1 < len(args) {
				resume = args[i+1]
				i++
			}
		case "--session-id":
			if i+1 < len(args) {
				sessionID = args[i+1]
				i++
			}
		case "-p", "--print":
			printMode = true
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				prompt = args[i+1]
				i++
			}
		default:
			if printMode && prompt == "" && !strings.HasPrefix(args[i], "-") {
				prompt = args[i]
			}
		}
	}
	if os.Getenv("FAKECLAUDE_FAIL") == "not-logged-in" {
		fmt.Fprintln(stdout, "Not logged in · Please run /login")
		return 1
	}
	home := os.Getenv("HOME")
	f.cfg = getenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "fakeclaude:", err)
		return 1
	}
	f.cwd = cwd
	proj := filepath.Join(f.cfg, "projects", session.Munge(cwd))
	switch {
	case resume != "":
		f.sid = strings.ToLower(resume)
		f.transcript = filepath.Join(proj, f.sid+".jsonl")
		if _, err := os.Stat(f.transcript); err != nil {
			fmt.Fprintf(stdout, "No conversation found with session ID: %s\n", resume)
			return 1
		}
		f.lastUUID = lastUUID(f.transcript)
	case sessionID != "":
		f.sid = strings.ToLower(sessionID)
		f.transcript = filepath.Join(proj, f.sid+".jsonl")
	default:
		f.sid = uuid4()
		f.transcript = filepath.Join(proj, f.sid+".jsonl")
	}
	if !session.IsUUID(f.sid) {
		fmt.Fprintf(stderr, "fakeclaude: invalid session id %q\n", f.sid)
		return 1
	}
	if err := os.MkdirAll(proj, 0o700); err != nil {
		fmt.Fprintln(stderr, "fakeclaude:", err)
		return 1
	}
	if resume == "" {
		// Claude Code opens every new session with a permission-mode record,
		// even before the first exchange; --resume's Stat above already
		// proved the transcript exists, so resumed sessions get no extra
		// startup record.
		f.append(map[string]any{
			"type": "permission-mode", "permissionMode": "default", "sessionId": f.sid, "timestamp": now(),
		})
	}
	f.procStart, _ = procx.StartTime("/proc", f.pid)
	f.startedAt = nowMS()
	f.tmux = tmuxRef()
	f.registry = filepath.Join(f.cfg, "sessions", strconv.Itoa(f.pid)+".json")
	if err := f.writeRegistry("busy"); err != nil {
		fmt.Fprintln(stderr, "fakeclaude:", err)
		return 1
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		os.Remove(f.registry)
		os.Exit(0)
	}()

	if child := os.Getenv("FAKECLAUDE_RUN_CHILD"); child != "" {
		// Deliberately a shell: this mimics Claude Code's `! <command>` bash
		// mode. The value comes from the test's own environment, never from
		// a user; fakeclaude is a test program, not part of the tool.
		c := exec.Command("sh", "-c", child)
		c.Env = append(os.Environ(), "CLAUDE_PID="+strconv.Itoa(f.pid), "CLAUDE_CODE_SESSION_ID="+f.sid, "CLAUDECODE=1")
		c.Stdout, c.Stderr = stdout, stderr
		c.Run()
	}
	if printMode {
		f.exchange(prompt)
		os.Remove(f.registry)
		return 0
	}
	f.writeRegistry("idle")
	fmt.Fprintf(stdout, "fakeclaude %s session %s in %s\n", f.version, f.sid, f.cwd)
	sc := bufio.NewScanner(stdin)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if line == "/exit" {
			break
		}
		f.writeRegistry("busy")
		f.exchange(line)
		f.writeRegistry("idle")
	}
	os.Remove(f.registry)
	return 0
}

// tmuxRef asks tmux for "<session>:@<win>.%<pane>" when running inside tmux.
func tmuxRef() string {
	if v := os.Getenv("FAKECLAUDE_TMUX"); v != "" {
		return v // Plan 03's fake tmux server: no tmux binary to ask
	}
	pane := os.Getenv("TMUX_PANE")
	if os.Getenv("TMUX") == "" || pane == "" {
		return ""
	}
	sock, _, _ := strings.Cut(os.Getenv("TMUX"), ",")
	out, err := exec.Command("tmux", "-S", sock, "display-message", "-p", "-t", pane, "#{session_name}:#{window_id}.#{pane_id}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (f *fake) writeRegistry(status string) error {
	ts := nowMS()
	rec := map[string]any{
		"pid": f.pid, "sessionId": f.sid, "cwd": f.cwd, "startedAt": f.startedAt, "procStart": f.procStart,
		"version": f.version, "kind": "interactive", "entrypoint": "cli", "tmux": f.tmux,
		"messagingSocketPath": filepath.Join(f.cfg, "sessions", strconv.Itoa(f.pid)+".sock"),
		"name":                filepath.Base(f.cwd), "nameSource": "auto", "status": status, "updatedAt": ts, "statusUpdatedAt": ts,
	}
	data, _ := json.Marshal(rec)
	return session.WriteFileAtomic(f.registry, data, 0o600)
}

func lastUUID(transcript string) string {
	data, err := os.ReadFile(transcript)
	if err != nil {
		return ""
	}
	last := ""
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var r struct {
			UUID string `json:"uuid"`
		}
		if json.Unmarshal([]byte(l), &r) == nil && r.UUID != "" {
			last = r.UUID
		}
	}
	return last
}

func (f *fake) append(rec map[string]any) {
	fh, err := os.OpenFile(f.transcript, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	defer fh.Close()
	enc := json.NewEncoder(fh)
	enc.SetEscapeHTML(false)
	enc.Encode(rec)
}

func (f *fake) base(typ string) map[string]any {
	parent := any(nil)
	if f.lastUUID != "" {
		parent = f.lastUUID
	}
	id := uuid4()
	rec := map[string]any{
		"parentUuid": parent, "isSidechain": false, "userType": "external", "cwd": f.cwd, "sessionId": f.sid,
		"version": f.version, "gitBranch": f.branch, "type": typ, "uuid": id, "timestamp": now(),
	}
	f.lastUUID = id
	return rec
}

// exchange appends one user turn and one assistant turn, and a history line.
func (f *fake) exchange(prompt string) {
	u := f.base("user")
	u["message"] = map[string]any{"role": "user", "content": prompt}
	f.append(u)
	hist := map[string]any{"display": prompt, "pastedContents": map[string]any{}, "timestamp": nowMS(), "project": f.cwd, "sessionId": f.sid}
	if hf, err := os.OpenFile(filepath.Join(f.cfg, "history.jsonl"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600); err == nil {
		json.NewEncoder(hf).Encode(hist)
		hf.Close()
	}
	reply := getenv("FAKECLAUDE_REPLY", "ok: "+prompt)
	a := f.base("assistant")
	a["requestId"] = "req_fake_" + strconv.FormatInt(nowMS(), 36)
	a["message"] = map[string]any{
		"id": "msg_fake_" + strconv.FormatInt(nowMS(), 36), "type": "message", "role": "assistant", "model": "claude-opus-4-1",
		"content":     []any{map[string]any{"type": "text", "text": reply}},
		"stop_reason": "end_turn", "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": 10, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0, "output_tokens": 5},
	}
	f.append(a)
}
