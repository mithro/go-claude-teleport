package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
	"github.com/mithro/go-claude-teleport/internal/transfer"
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
	if e := callOp(t, ep, OpSessionExtras, sessionExtrasArgs{ID: session.ID(sid), PathMap: pm}, &out); e != nil {
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

// TestServeGitFilesOp covers the "git-files" op: gitx.Files walks
// SrcWorktree for Mode: ModeNotRepo (no git repo needed), returning the
// root dir entry (Rel: "") plus one entry per file.
func TestServeGitFilesOp(t *testing.T) {
	p := testPaths(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	ep := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc"})
	plan := &gitx.Plan{Mode: gitx.ModeNotRepo, SrcWorktree: dir, PackEntryID: gitx.NoEntry, IndexEntryID: gitx.NoEntry}
	var out filesResult
	if e := callOp(t, ep, OpGitFiles, gitFilesArgs{Plan: plan}, &out); e != nil {
		t.Fatal(e)
	}
	var gotFile bool
	for _, f := range out.Files {
		if f.Rel == "a.txt" {
			gotFile = true
		}
	}
	if !gotFile {
		t.Errorf("files = %+v, want a.txt", out.Files)
	}
}

// TestServeGitSourceFactsOp covers the "git-source-facts" op against a real
// git repo: destTip == tip is trivially reachable, and a clean commit
// leaves no staged blobs the tip's tree doesn't already have.
func TestServeGitSourceFactsOp(t *testing.T) {
	p := testPaths(t)
	repo := t.TempDir()
	gitc(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a"), []byte("a"), 0o644)
	gitc(t, repo, "add", "a")
	gitc(t, repo, "commit", "-q", "-m", "i")
	tip := strings.TrimSpace(gitc(t, repo, "rev-parse", "HEAD"))
	ep := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc"})
	var facts gitx.SourceFacts
	if e := callOp(t, ep, OpGitSourceFacts, gitSourceFactsArgs{MainDir: repo, IndexRel: ".git/index", Tip: tip, DestTip: tip}, &facts); e != nil {
		t.Fatal(e)
	}
	if !facts.DestTipReachable {
		t.Errorf("facts = %+v, want DestTipReachable", facts)
	}
	if len(facts.StagedBlobs) != 0 {
		t.Errorf("facts.StagedBlobs = %v, want none after a clean commit", facts.StagedBlobs)
	}
}

// TestServeTmuxSessionsOp covers the "tmux-sessions" op against a fake
// tmux transport.
func TestServeTmuxSessionsOp(t *testing.T) {
	p := testPaths(t)
	f := &tmuxx.Fake{Replies: map[string][]string{
		"list-sessions -F \"#{session_name}\t#{session_group}\"": {"work\twork"},
	}}
	ep := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc", Tmux: fakeDialer(f)})
	var out tmuxSessionsResult
	if e := callOp(t, ep, OpTmuxSessions, tmuxSessionsArgs{SocketPath: "/s"}, &out); e != nil {
		t.Fatal(e)
	}
	if len(out.Sessions) != 1 || out.Sessions[0].Name != "work" || out.Sessions[0].Group != "work" {
		t.Errorf("sessions = %+v", out.Sessions)
	}
}

// TestServeTmuxKillOp covers the "tmux-kill" op against a fake tmux
// transport.
func TestServeTmuxKillOp(t *testing.T) {
	p := testPaths(t)
	f := &tmuxx.Fake{Replies: map[string][]string{`kill-window -t "@1"`: {}}}
	ep := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc", Tmux: fakeDialer(f)})
	ref := &session.TmuxRef{SocketPath: "/s", WindowID: "@1"}
	if e := callOp(t, ep, OpKillWindow, killWindowArgs{Ref: ref}, nil); e != nil {
		t.Fatal(e)
	}
}

// TestServeClaudeStatusOp covers the "claude-status" op: absent before a
// live registry entry exists, present (with the registry row) after.
func TestServeClaudeStatusOp(t *testing.T) {
	p := testPaths(t)
	ep := NewLocal(p, "x", LocalOptions{ProcRoot: fakeProcRoot(t, [][4]string{{"5150", "1", "claude", "claude\x00"}})})
	var absent claudeStatusResult
	if e := callOp(t, ep, OpClaudeStatus, claudeStatusArgs{ID: session.ID(sid)}, &absent); e != nil {
		t.Fatal(e)
	}
	if absent.OK {
		t.Errorf("absent = %+v, want not ok", absent)
	}
	writeRegistry(t, p, 5150, "idle", "")
	var present claudeStatusResult
	if e := callOp(t, ep, OpClaudeStatus, claudeStatusArgs{ID: session.ID(sid)}, &present); e != nil {
		t.Fatal(e)
	}
	if !present.OK || present.Registry == nil || present.Registry.Status != "idle" {
		t.Errorf("present = %+v", present)
	}
}

// TestServeBuildManifestOp covers the "build-manifest" op: the returned
// manifest is hashed and jobs/<job>/manifest.json is persisted on this host.
func TestServeBuildManifestOp(t *testing.T) {
	p := testPaths(t)
	cwd := filepath.Join(p.Home, "x")
	proj := p.ProjectDir(cwd)
	os.MkdirAll(proj, 0o700)
	os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(`{"cwd":"`+cwd+`"}`+"\n"), 0o600)
	files := []session.FileEntry{{Root: p.ConfigDir, Rel: "projects/" + session.Munge(cwd) + "/" + sid + ".jsonl", Category: session.CatSession, Mode: 0o600, Rewrite: true}}
	pm := session.NewPathMap(session.Mapping{From: p.Home, To: "/home/bob-dest"})
	ep := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc"})
	const jobID = "buildmanjob"
	var m transfer.Manifest
	if e := callOp(t, ep, OpBuildManifest, buildManifestArgs{JobID: jobID, ID: session.ID(sid), SrcHost: "laptop.example", DstHost: "big-storage.example", Files: files, PathMap: pm}, &m); e != nil {
		t.Fatal(e)
	}
	if len(m.Entries) != 1 || m.Entries[0].SHA256 == "" {
		t.Errorf("manifest = %+v", m)
	}
	if _, err := os.Stat(filepath.Join(job.Dir(p.DataDir, jobID), "manifest.json")); err != nil {
		t.Errorf("manifest.json not persisted: %v", err)
	}
}

// TestServeCleanupOp covers the "cleanup" op: staging/<job> is removed.
func TestServeCleanupOp(t *testing.T) {
	p := testPaths(t)
	ep := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc"})
	const jobID = "cleanjob"
	staging := job.StagingDir(p.DataDir, jobID)
	os.MkdirAll(staging, 0o700)
	os.WriteFile(filepath.Join(staging, "0"), []byte("x"), 0o600)
	if e := callOp(t, ep, OpCleanup, cleanupArgs{JobID: jobID}, nil); e != nil {
		t.Fatal(e)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Errorf("staging still exists after cleanup: %v", err)
	}
}

// TestServeListSessionsOp covers the "list-sessions" op wire path; deeper
// registry/alive-filter/sort coverage lives in local_transfer_test.go
// against Local.ListSessions directly.
func TestServeListSessionsOp(t *testing.T) {
	p := testPaths(t)
	cwd := filepath.Join(p.Home, "x")
	proj := p.ProjectDir(cwd)
	os.MkdirAll(proj, 0o700)
	os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(`{"type":"user","cwd":"`+cwd+`","timestamp":"2026-01-01T00:00:00Z"}`+"\n"), 0o600)
	ep := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc"})
	var out sessionsResult
	if e := callOp(t, ep, OpListSessions, struct{}{}, &out); e != nil {
		t.Fatal(e)
	}
	if len(out.Sessions) != 1 || out.Sessions[0].ID != session.ID(sid) || out.Sessions[0].Cwd != cwd {
		t.Errorf("sessions = %+v", out.Sessions)
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

// TestPlan03OpsServeThroughAChainedClient is the I5 regression: the nine
// Plan 03 ops are on Endpoint and dispatched like every other op, so a
// Server can serve them from a *Client* (a chained/proxied host) — the old
// `ep.(*Local)` branch in handle failed all nine outright there. The chain
// is: outer Client -> Serve(inner Client) -> Serve(Local).
func TestPlan03OpsServeThroughAChainedClient(t *testing.T) {
	p := testPaths(t)
	cwd := filepath.Join(p.Home, "x")
	proj := p.ProjectDir(cwd)
	os.MkdirAll(proj, 0o700)
	os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(`{"type":"user","cwd":"`+cwd+`","timestamp":"2026-01-01T00:00:00Z"}`+"\n"), 0o600)
	f := &tmuxx.Fake{Replies: map[string][]string{
		"list-sessions -F \"#{session_name}\t#{session_group}\"": {"work\twork"},
		`kill-window -t "@1"`: {},
	}}
	l := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc", Tmux: fakeDialer(f)})
	chained := pipeEndpointClient(t, pipeEndpointClient(t, l))
	ctx := context.Background()

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	files, err := chained.GitFiles(ctx, &gitx.Plan{Mode: gitx.ModeNotRepo, SrcWorktree: dir, PackEntryID: gitx.NoEntry, IndexEntryID: gitx.NoEntry}, nil, false)
	if err != nil || len(files) == 0 {
		t.Errorf("GitFiles = %v %v", files, err)
	}
	sessions, err := chained.TmuxSessions(ctx, "/s")
	if err != nil || len(sessions) != 1 || sessions[0].Name != "work" {
		t.Errorf("TmuxSessions = %+v %v", sessions, err)
	}
	if err := chained.KillWindow(ctx, &session.TmuxRef{SocketPath: "/s", WindowID: "@1"}); err != nil {
		t.Errorf("KillWindow = %v", err)
	}
	if _, ok, err := chained.ClaudeStatus(ctx, session.ID(sid)); err != nil || ok {
		t.Errorf("ClaudeStatus = %v %v, want not ok", ok, err)
	}
	if err := chained.Cleanup(ctx, "nosuchjob"); err != nil {
		t.Errorf("Cleanup = %v", err)
	}
	sums, err := chained.ListSessions(ctx)
	if err != nil || len(sums) != 1 || sums[0].ID != session.ID(sid) {
		t.Errorf("ListSessions = %+v %v", sums, err)
	}
	// The remaining three (git-source-facts, build-manifest, session-extras)
	// need on-disk git/transcript fixtures and are covered against Serve
	// above; all nine share one Op* constant between handler and Client, so
	// the two sides cannot name different ops.
}

// TestPlan03OpsAreInTheSingleDispatchTable pins that the merge happened and
// that no op was left behind in a second table.
func TestPlan03OpsAreInTheSingleDispatchTable(t *testing.T) {
	for _, op := range []string{OpGitFiles, OpGitSourceFacts, OpTmuxSessions, OpKillWindow,
		OpClaudeStatus, OpBuildManifest, OpSessionExtras, OpCleanup, OpListSessions} {
		if dispatch[op] == nil {
			t.Errorf("op %q is not in dispatch", op)
		}
	}
}
