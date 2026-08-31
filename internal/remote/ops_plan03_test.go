package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// callOp drives Serve over in-memory pipes and returns a Client-like caller
// for one op, decoding into result. (Named distinctly from server_test.go's
// roundTrip, which drives multiple raw request lines at once.)
func callOp(t *testing.T, ep Endpoint, op string, args any, result any) *Error {
	t.Helper()
	cr, cw := io.Pipe() // client -> server
	sr, sw := io.Pipe() // server -> client
	go Serve(context.Background(), cr, sw, ep)
	a, _ := json.Marshal(args)
	req, _ := json.Marshal(Request{ID: 1, Op: op, Args: a})
	go func() { cw.Write(append(req, '\n')); cw.Close() }()
	line, err := bufio.NewReader(sr).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		return resp.Error
	}
	if result != nil {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			t.Fatal(err)
		}
	}
	return nil
}

// TestServeInventoryGitOp proves a Plan 02 op (already dispatched, not part
// of plan03Ops) still round-trips correctly through Serve using its real
// wire types (InventoryGitArgs/InventoryGitResult).
func TestServeInventoryGitOp(t *testing.T) {
	p := testPaths(t)
	repo := filepath.Join(p.Home, "x")
	os.MkdirAll(repo, 0o755)
	gitc(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a"), []byte("a"), 0o644)
	gitc(t, repo, "add", "a")
	gitc(t, repo, "commit", "-q", "-m", "i")
	ep := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc"})
	var out InventoryGitResult
	if e := callOp(t, ep, OpInventoryGit, InventoryGitArgs{Cwd: repo}, &out); e != nil {
		t.Fatal(e)
	}
	if out.Info.Branch != "main" {
		t.Errorf("info = %+v", out.Info)
	}
	if e := callOp(t, ep, OpInventoryGit, InventoryGitArgs{Cwd: t.TempDir()}, nil); e == nil || e.Code != "not-found" {
		t.Errorf("non-repo over the wire = %v", e)
	}
}

func TestServeSessionExtrasOp(t *testing.T) {
	p := testPaths(t)
	cwd := filepath.Join(p.Home, "x")
	proj := p.ProjectDir(cwd)
	os.MkdirAll(proj, 0o700)
	// session.Load (called by SessionExtras) requires the transcript itself
	// to exist (FindTranscript globs projectsDir/*/<id>.jsonl); the brief's
	// index-only fixture is not enough on its own.
	os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(`{"cwd":"`+cwd+`"}`+"\n"), 0o600)
	os.WriteFile(filepath.Join(proj, "sessions-index.json"), []byte(`{"version":1,"entries":[{"sessionId":"`+sid+`","fullPath":"`+proj+`/`+sid+`.jsonl","projectPath":"`+cwd+`","messageCount":1}],"originalPath":"`+cwd+`"}`), 0o600)
	os.WriteFile(p.HistoryFile(), []byte(`{"display":"hi","timestamp":1,"project":"`+cwd+`","sessionId":"`+sid+`"}`+"\n"), 0o600)
	os.WriteFile(p.GlobalJSON, []byte(`{"projects":{"`+cwd+`":{"hasTrustDialogAccepted":true}}}`), 0o600)
	ep := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc"})
	pm := session.NewPathMap(session.Mapping{From: p.Home, To: "/home/bob"})
	var out extrasResult
	if e := callOp(t, ep, "session-extras", sessionExtrasArgs{ID: session.ID(sid), PathMap: pm}, &out); e != nil {
		t.Fatal(e)
	}
	if out.Extras.IndexEntry == nil || out.Extras.IndexEntry.ProjectPath != "/home/bob/x" {
		t.Errorf("index entry = %+v", out.Extras.IndexEntry)
	}
	if len(out.Extras.History) != 1 || !containsBytes(out.Extras.History[0], "/home/bob/x") {
		t.Errorf("history = %s", out.Extras.History)
	}
	if out.Extras.ProjectCwd != "/home/bob/x" || out.Extras.ProjectEntry["hasTrustDialogAccepted"] != true {
		t.Errorf("project = %q %v", out.Extras.ProjectCwd, out.Extras.ProjectEntry)
	}
}

func containsBytes(b json.RawMessage, s string) bool { return len(b) > 0 && bytesContains(b, s) }

func bytesContains(b []byte, s string) bool {
	return len(s) == 0 || len(b) >= len(s) && func() bool {
		for i := 0; i+len(s) <= len(b); i++ {
			if string(b[i:i+len(s)]) == s {
				return true
			}
		}
		return false
	}()
}
