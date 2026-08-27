# claude-teleport — package interfaces (shared by all implementation plans)

**Spec:** `docs/superpowers/specs/2026-08-27-claude-teleport-design.md`

This document pins the Go package boundaries and the exact exported names,
types and signatures that the three implementation plans build against:

- Plan 01 — foundation and local model (`cli`, `session`, `claudecfg`,
  `procx`, `placeholder`, `test/fakeclaude`, CI + release pipeline)
- Plan 02 — transport (`sshx`, `remote`, `transfer`, `job`, `fakeapi`)
- Plan 03 — git, tmux, orchestrator, docker integration, README

A plan may add unexported helpers and extra exported functions, but must not
rename or re-type anything listed here. If a plan finds an interface
insufficient it extends it (adds a field or method) and records the addition
at the end of its own document under "Interface additions".

## Global constraints (from the spec)

- Module `github.com/mithro/go-claude-teleport`; binary `claude-teleport`;
  Go `1.26`; `CGO_ENABLED=0`; Apache-2.0.
- Dependencies allowed: `golang.org/x/crypto`, `github.com/kevinburke/ssh_config`,
  `github.com/go-git/go-git/v5`, `github.com/spf13/cobra`,
  `github.com/google/go-cmp` (tests). Nothing else without a stated reason in
  the plan.
- No `ssh`, `rsync`, `tar`, `gzip`, `git` subprocesses in the tool. `tmux -C`
  (control mode) and `claude --version` (preflight only) are the only
  subprocesses.
- Never read `.credentials.json`, `sessions/*.key`, or token fields.
- Every exported function that touches the filesystem takes explicit
  directories (config dir, home, data dir) — never `os.UserHomeDir()`
  inside a package; only `internal/cli` resolves defaults from the
  environment.
- Errors wrap with `%w` and carry the path/pid/op involved. No silent
  fallbacks: a missing prerequisite is an error, not a default.
- Tests: stdlib `testing`, `go-cmp`; fixtures in `testdata/`; sanitised
  paths (`/home/alice`), hosts (`*.example`), fresh uuids.
- Dates in docs/logs: ISO 8601.

## Directory layout

```
cmd/claude-teleport/main.go
internal/cli/                  (Plan 01, extended by 02/03)
internal/session/              (Plan 01)
internal/claudecfg/            (Plan 01)
internal/procx/                (Plan 01)
internal/placeholder/          (Plan 01)
internal/version/              (Plan 01)  var Version = "dev" (ldflags -X)
test/fakeclaude/               (Plan 01)
internal/sshx/                 (Plan 02)
internal/remote/               (Plan 02)
internal/transfer/             (Plan 02)
internal/job/                  (Plan 02)
internal/fakeapi/              (Plan 02)
internal/gitx/                 (Plan 03)
internal/tmuxx/                (Plan 03; tmuxctl copied from go-tmux-saver)
internal/orchestrate/          (Plan 03)
test/integration/              (Plan 03)
packaging/, nfpm.yaml, .github/workflows/   (Plan 01)
```

Data directories (`internal/cli` computes them, packages receive them):

- config dir: `$CLAUDE_CONFIG_DIR` or `$HOME/.claude`
- global json: `$HOME/.claude.json` (when `CLAUDE_CONFIG_DIR` is set Claude
  keeps `.claude.json` *inside* that dir — verify in Plan 01 spike and
  encode in `session.Paths`)
- data dir: `$XDG_DATA_HOME/claude-teleport` or `$HOME/.local/share/claude-teleport`
  (`jobs/<sid>/`, `staging/<sid>/`)

## internal/session

```go
package session

type ID string                       // canonical lowercase uuid
func ParseID(s string) (ID, error)   // full uuid only
func (id ID) Short() string          // first 8 chars

// Munge mirrors Claude Code's project-dir naming: '/' and '.' -> '-'.
func Munge(absPath string) string

// Paths resolves the on-disk locations for one config dir / home pair.
type Paths struct {
    Home       string // $HOME on this host
    ConfigDir  string // ~/.claude or $CLAUDE_CONFIG_DIR
    GlobalJSON string // ~/.claude.json (or <ConfigDir>/.claude.json, see spike)
    DataDir    string // claude-teleport data dir
}
func (p Paths) ProjectsDir() string
func (p Paths) SessionsDir() string      // registry dir
func (p Paths) HistoryFile() string
func (p Paths) ProjectDir(cwd string) string  // ProjectsDir()/Munge(cwd)

type State int
const (
    StateIdle State = iota      // transcript on disk, no process, no placeholder pane
    StateRunning                // live claude process (registry entry)
    StateSuspended              // a pane whose foreground command is a placeholder
)
func (s State) String() string  // "idle" | "running" | "suspended"

// Registry is ~/.claude/sessions/<pid>.json (only the fields we use).
type Registry struct {
    PID       int    `json:"pid"`
    SessionID string `json:"sessionId"`
    Cwd       string `json:"cwd"`
    ProcStart string `json:"procStart"`   // string OR number in the file; normalised to string
    Version   string `json:"version"`
    Kind      string `json:"kind"`
    Status    string `json:"status"`      // "busy" | "idle"
    Tmux      string `json:"tmux"`        // "<session>:@<win>.%<pane>" or ""
    Name      string `json:"name"`
    UpdatedAt int64  `json:"updatedAt"`
    File      string `json:"-"`           // path it was read from
}
func ReadRegistry(sessionsDir string) ([]Registry, error)          // all *.json; malformed files are errors listing the path
func (r Registry) TmuxParts() (sess, windowID, paneID string, ok bool)

// TmuxRef is where a session's pane lives (from the registry or a pane scan).
type TmuxRef struct {
    SocketPath string
    Session    string   // session name
    WindowID   string   // "@N"
    PaneID     string   // "%N"
}

type Session struct {
    ID         ID
    Paths      Paths
    ProjectDir string   // <ProjectsDir>/<munged launch cwd>
    Transcript string   // <ProjectDir>/<id>.jsonl
    LaunchCwd  string   // first cwd in the transcript
    WorkCwd    string   // last cwd in the transcript
    Branch     string   // last gitBranch
    Name       string   // registry name if running, else ""
    Version    string   // claude version from the transcript (last "version")
    State      State
    Registry   *Registry // non-nil iff StateRunning
    Tmux       *TmuxRef  // non-nil when a pane is known (running or suspended)
}

// Selector is the parsed positional/environment session selection (spec §5).
type Selector struct {
    Current    bool     // no args and $CLAUDE_CODE_SESSION_ID / $TMUX_PANE available
    ID         ID       // full uuid
    Prefix     string   // >=4 hex chars, or a registry name
    TmuxSess   string   // two-word form
    TmuxWindow string
}
type Env struct {
    SessionID string // $CLAUDE_CODE_SESSION_ID
    PID       string // $CLAUDE_PID
    TmuxPane  string // $TMUX_PANE
    Tmux      string // $TMUX
}
func ParseSelector(args []string, env Env) (Selector, error)

// PaneProbe lets Resolve consult tmux without importing tmuxx (Plan 03 wires
// tmuxx.Prober in; Plan 01 tests use a fake).
type PaneProbe interface {
    // PaneCommand returns the foreground command line (argv) and pid of the
    // pane; ok=false if the pane cannot be found.
    PaneCommand(paneID string) (argv []string, pid int, ok bool)
    // FindWindow resolves "<session> <window index|name>" to its pane ids.
    FindWindow(session, window string) (paneIDs []string, err error)
    SocketPath() string
}

// Resolve turns a selector into a Session, scanning the registry, the
// projects tree and (optionally) tmux panes. Ambiguity is an error listing
// the candidates; not found is ErrNotFound.
func Resolve(p Paths, sel Selector, probe PaneProbe) (*Session, error)
var ErrNotFound = errors.New("session not found")

// Load reads an already-known session (by id) from disk; State is Idle
// unless the registry/probe says otherwise.
func Load(p Paths, id ID, probe PaneProbe) (*Session, error)

// Meta is the human context pulled from the transcript (port of
// go-tmux-saver resume.ReadMeta).
type Meta struct {
    Summary, Title, FirstUser string
    LaunchCwd, WorkCwd, Branch string
    Version   string
    LastTS    string
}
func ReadMeta(transcript string) (Meta, error)
func (m Meta) Label() string

// Category classifies transferred files (spec §7.1).
type Category string
const (
    CatSession  Category = "session"
    CatRepo     Category = "repo"
    CatWorktree Category = "worktree"
    CatCapture  Category = "capture"
    CatPack     Category = "pack"
)

// FileEntry is one file/dir/symlink to move. Rel is relative to Root so the
// destination path is Root' + Rel after rewriting Root.
type FileEntry struct {
    Root     string      // absolute root this entry belongs to (e.g. ConfigDir or repo dir)
    Rel      string      // slash-separated, relative to Root ("" for the root dir itself)
    Category Category
    Size     int64
    Mode     fs.FileMode
    ModTime  time.Time
    Symlink  string      // link target if a symlink
    Rewrite  bool        // JSON content must go through the path map
}
func (e FileEntry) Path() string   // filepath.Join(Root, Rel)

// Inventory lists every session file to move (spec §3 table, "yes" rows).
// Forbidden paths are never returned; the returned Skipped lists sockets,
// fifos, the tasks .lock file, and anything unreadable (with the error).
type Inventory struct {
    Files   []FileEntry
    Skipped []Skipped
    Memory  []FileEntry   // projects/<munged>/memory/** (copied only if absent on dest)
}
type Skipped struct{ Path, Reason string }
func InventoryFiles(s *Session) (*Inventory, error)

// Forbidden reports whether rel (relative to ConfigDir) may never be moved.
func Forbidden(rel string) bool

// Usage is what the session actually used (spec §10).
type Usage struct {
    MCPServers    map[string]bool // from tool_use names mcp__<server>__<tool>
    Skills        map[string]bool // Skill tool "skill" arg + attributionSkill
    Plugins       map[string]bool // attributionPlugin
    SubagentTypes map[string]bool // Agent tool subagent_type
    PermissionModes map[string]bool // permission-mode records
}
func ScanUsage(s *Session) (*Usage, error) // main transcript + subagents/*.jsonl

// PathMap is an ordered prefix rewrite (longest prefix first; spec §7.2).
type Mapping struct{ From, To string }
type PathMap []Mapping
func NewPathMap(maps ...Mapping) PathMap        // sorts by len(From) desc, validates absolute, no dup From
func (m PathMap) Apply(s string) string          // rewrites the first matching prefix at a path boundary
func (m PathMap) ApplyPath(p string) string      // same, for a whole path (boundary = "/" or end)
func (m PathMap) Empty() bool

// RewriteStats reports what a rewrite touched.
type RewriteStats struct{ Records, Rewritten, Unparseable int }
// RewriteJSONL streams r to w, decoding each line, rewriting every string
// value, re-encoding with SetEscapeHTML(false) and UseNumber; unparseable
// lines are copied verbatim and counted.
func RewriteJSONL(r io.Reader, w io.Writer, m PathMap) (RewriteStats, error)
// RewriteJSON does the same for a single JSON document.
func RewriteJSON(r io.Reader, w io.Writer, m PathMap) (RewriteStats, error)

// IsPrefix reports whether file `existing` is a byte-prefix of `incoming`
// (streaming; spec §7.3 fast-forward rule).
func IsPrefix(existing, incoming string) (bool, error)

// Index/history merges (spec §7.5).
type IndexEntry struct {
    SessionID    string `json:"sessionId"`
    FullPath     string `json:"fullPath"`
    FileMtime    int64  `json:"fileMtime"`
    FirstPrompt  string `json:"firstPrompt"`
    Summary      string `json:"summary"`
    MessageCount int    `json:"messageCount"`
    Created      string `json:"created"`
    Modified     string `json:"modified"`
    GitBranch    string `json:"gitBranch"`
    ProjectPath  string `json:"projectPath"`
    IsSidechain  bool   `json:"isSidechain"`
}
func ReadIndexEntry(projectDir string, id ID) (*IndexEntry, bool, error)
func MergeIndexEntry(projectDir string, e IndexEntry) error   // add or replace by sessionId; creates the file if absent; atomic write
func ExtractHistory(historyFile string, id ID) ([]json.RawMessage, error)
func AppendHistory(historyFile string, lines []json.RawMessage) (added int, err error) // dedupe on timestamp+sessionId

// GlobalJSON: the ~/.claude.json project entry (spec §3).
type ProjectEntry = map[string]any   // opaque; only copied whole
func ReadProjectEntry(globalJSON, cwd string) (ProjectEntry, bool, error)
func AddProjectEntry(globalJSON, cwd string, e ProjectEntry) (added bool, err error) // no-op if present; backup + temp + rename
```

## internal/claudecfg

```go
package claudecfg

type PluginInfo struct {
    Version   string
    HooksHash string // sha256 of hooks/hooks.json, "" if none
    MCPHash   string // sha256 of .mcp.json, "" if none
}
type Permissions struct {
    DefaultMode string
    Allow, Deny []string
}
type Inventory struct {
    Host           string
    ClaudeVersion  string
    Hooks          string            // canonical JSON of settings.hooks ("" if absent)
    Permissions    Permissions
    Env            map[string]string
    EnabledPlugins map[string]bool
    Model, Effort  string
    MCPServers     map[string]string // name -> canonical JSON config (user level)
    ProjectPresent bool
    ProjectMCP     map[string]string // projects[cwd].mcpServers
    ProjectEnabledMCPJSON, ProjectDisabledMCPJSON []string
    AllowedTools   []string
    Plugins        map[string]PluginInfo // "name@marketplace" -> info
    TreeHashes     map[string]string     // "CLAUDE.md", "agents", "skills", "commands"
    KeybindingsHash string
}
// Collect reads the host's configuration. claudeVersion is supplied by the
// caller (registry or `claude --version`). Missing files are not errors;
// malformed files are.
func Collect(p session.Paths, cwd, host, claudeVersion string) (*Inventory, error)

type Class int
const (
    Info Class = iota
    Warn
    Block
)
func (c Class) String() string
type Difference struct {
    Class  Class
    Key    string   // e.g. "hooks", "mcp.playwright", "plugin.superpowers@claude-plugins-official"
    Source string   // short rendering
    Dest   string
    Reason string
}
type Report struct {
    Diffs    []Difference
    Blocking bool           // any Block
}
// Compare classifies differences per spec §10. usage==nil means "everything used".
func Compare(src, dst *Inventory, usage *session.Usage) Report
func (r Report) Downgrade() Report      // --allow-config-drift: Block -> Warn
func (r Report) Render(w io.Writer)     // table
func (r Report) JSON() ([]byte, error)
```

## internal/procx

```go
package procx

type Proc struct {
    PID, PPID int
    Comm      string
    Cmdline   []string
    StartTime string // field 22 of /proc/<pid>/stat, as a string
}
type Table struct{ /* ... */ }
func Scan(procRoot string) (*Table, error)         // "/proc" in production
func (t *Table) Get(pid int) (Proc, bool)
func (t *Table) Children(pid int) []int
func (t *Table) Subtree(pid int) []int             // BFS incl. pid
func (t *Table) Alive(pid int, startTime string) bool

// Registry lookup helpers (thin wrappers over session.ReadRegistry).
func RegistryForPID(sessionsDir string, pid int, startTime string) (*session.Registry, bool, error)
func RegistryForSession(sessionsDir string, id session.ID) (*session.Registry, bool, error)

// Freezer stops a pid and guarantees a thaw when the owner dies (spec §6.1).
// Implementation: re-exec self as `claude-teleport internal-freezer <pid> <start>`
// with a pipe on fd 3; the child SIGSTOPs, blocks on the pipe, SIGCONTs on
// any read result (data or EOF).
type Freezer struct{ /* ... */ }
func Freeze(selfExe string, pid int, startTime string) (*Freezer, error)
func (f *Freezer) Thaw() error       // writes "thaw\n", waits for the helper to exit
func RunFreezerHelper(pid int, startTime string, control *os.File) error // the helper's main

// WaitGone polls until pid (with startTime) is no longer alive.
func WaitGone(t func() (*Table, error), pid int, startTime string, timeout, poll time.Duration, sleep func(time.Duration)) error

// SpawnDetached starts argv in its own session (setsid) with stdin from
// /dev/null and stdout+stderr appended to logPath; returns the child pid.
func SpawnDetached(argv []string, dir, logPath string, env []string) (int, error)

// IsPlaceholderArgv recognises claude-resume/<self> placeholder command
// lines and returns the session id they hold.
func IsPlaceholderArgv(argv []string) (sid string, ok bool)
// IsClaudeArgv recognises a real claude process, returning --resume id if any.
func IsClaudeArgv(argv []string) (resumeID string, ok bool)
```

## internal/placeholder

```go
package placeholder

type Options struct {
    SessionID    string
    SavedOutput  string  // file to print above the banner ("" = none)
    Now          bool    // skip the confirm wait
    TeleportedTo string
    TeleportedAt string  // ISO 8601
    ProjectsDir  string
    Home         string
}
type Decision struct {
    Argv  []string // claude --resume <sid>   (or claude)
    Chdir string
    Skip  bool
}
// Decide renders the banner and waits (unless Now or stdin is not a tty).
func Decide(w io.Writer, o Options, stdoutTTY, stdinTTY bool, readLine func() (string, error)) Decision
func Render(w io.Writer, o Options, meta *session.Meta, tty bool)
func ChdirTarget(m session.Meta, transcript string) string
```

## internal/version

```go
package version
var Version = "dev"          // set by -ldflags "-X .../internal/version.Version=vX.Y"
const Protocol = 1           // remote protocol version
```

## internal/sshx (Plan 02)

```go
package sshx

type Target struct {
    User string
    Host string   // as typed (alias) — resolved HostName lives in Resolved
    Port int
    Via  []Target // jump chain, outermost first
}
type Resolved struct {
    Target
    HostName      string   // from ssh_config HostName or Host
    IdentityFiles []string
    Options       map[string]string // remaining -o overrides
}
// ParseTarget parses "[user@]host[:port]"; via entries are parsed the same way.
func ParseTarget(s string) (Target, error)
// Resolve applies ~/.ssh/config (Host/HostName/User/Port/IdentityFile/ProxyJump)
// and -o overrides. ProxyJump from config is prepended to Via.
func Resolve(t Target, cfg *ssh_config.Config, overrides map[string]string, localUser string) (Resolved, error)

type Options struct {
    KnownHostsFile string
    AgentSocket    string          // $SSH_AUTH_SOCK
    StrictHostKey  string          // "yes" (default) | "accept-new" | "no"
    ConnectTimeout time.Duration
    Logf           func(string, ...any)
}
type Client struct{ /* wraps *ssh.Client and the jump clients */ }
// Dial connects through the jump chain; each hop's hostname is resolved by
// the previous hop (client.Dial("tcp", host:port)).
func Dial(ctx context.Context, r Resolved, cfg *ssh_config.Config, overrides map[string]string, o Options) (*Client, error)
func (c *Client) Close() error
func (c *Client) String() string // user@host (via a, b)

type Process struct {
    Stdin  io.WriteCloser
    Stdout io.Reader
    Stderr io.Reader
    Wait   func() error   // *ssh.ExitError on non-zero exit
    Close  func() error
}
func (c *Client) Start(ctx context.Context, cmd string) (*Process, error)
func (c *Client) StartPty(ctx context.Context, cmd string, rows, cols int) (*Process, error)
// Run is Start + drain; returns stdout, stderr, error (ExitError wrapped).
func (c *Client) Run(ctx context.Context, cmd string, stdin io.Reader) ([]byte, []byte, error)
// Quote renders argv for the remote sh.
func Quote(argv []string) string
```

## internal/remote (Plan 02)

The Endpoint interface is the seam between the orchestrator and either host.

```go
package remote

type HostInfo struct {
    Version   string // claude-teleport version
    Protocol  int
    Hostname  string
    OS, Arch  string
    UID       int
    Home      string
    ConfigDir string
    DataDir   string
    TmuxSocketDir string
    HasTmux   bool
    HasClaude bool
    ClaudeVersion string
    HasClaudeResume bool // go-tmux-saver's claude-resume on PATH
}

// StreamKind names the bulk channels.
type StreamKind string
const (
    StreamTar     StreamKind = "tar"      // driver -> dest: transfer stream
    StreamCapture StreamKind = "capture"  // source -> driver: pane capture
    StreamPack    StreamKind = "pack"     // source -> driver: git packfile
    StreamLog     StreamKind = "log"      // remote job log tail
)

// Endpoint is every operation the orchestrator performs on a host.
// Local implements it directly; Client implements it over the protocol;
// Server dispatches protocol requests to a Local.
type Endpoint interface {
    Hello(ctx context.Context) (HostInfo, error)
    Paths() session.Paths

    // inventories
    ResolveSession(ctx context.Context, sel session.Selector) (*session.Session, error)
    InventorySession(ctx context.Context, id session.ID) (*session.Inventory, *session.Usage, error)
    InventoryHost(ctx context.Context, cwd, claudeVersion string) (*claudecfg.Inventory, error)
    InventoryGit(ctx context.Context, cwd string) (*gitx.Info, error)          // gitx types defined in Plan 03; Plan 02 stubs with an opaque json.RawMessage until then
    GitDestState(ctx context.Context, mainDir, worktreeDir, branch string) (*gitx.DestState, error)
    InventoryTmux(ctx context.Context, ref *session.TmuxRef, preferredSocket string) (*tmuxx.Facts, error)

    // transfer
    ManifestDiff(ctx context.Context, m *transfer.Manifest, jobID string) (map[int]transfer.Status, error)
    OpenStream(ctx context.Context, kind StreamKind, jobID, streamID string) (io.ReadWriteCloser, error)
    Install(ctx context.Context, m *transfer.Manifest, jobID string) (*transfer.InstallReport, error)
    GitAttach(ctx context.Context, plan *gitx.Plan, jobID string) error

    // processes and panes
    Freeze(ctx context.Context, pid int, startTime string) error
    Thaw(ctx context.Context, pid int) error
    Capture(ctx context.Context, ref *session.TmuxRef, jobID string) error   // writes jobs/<id>/capture.txt on that host
    OpenWindow(ctx context.Context, p *tmuxx.Plan) (*session.TmuxRef, error)
    StartClaude(ctx context.Context, ref *session.TmuxRef, id session.ID, jobID string, argv []string) error
    ConfirmClaude(ctx context.Context, ref *session.TmuxRef, id session.ID, timeout time.Duration) (*session.Registry, error)
    ExitClaude(ctx context.Context, ref *session.TmuxRef, pid int, startTime string, timeout time.Duration) error
    TypeCommand(ctx context.Context, ref *session.TmuxRef, argv []string) error
    PaneState(ctx context.Context, ref *session.TmuxRef) (*tmuxx.PaneState, error)
    RunPtyResume(ctx context.Context, id session.ID, cwd string, timeout time.Duration) error // no-tmux confirmation (spec §9)

    // journal
    JournalGet(ctx context.Context, jobID string) (*job.Journal, bool, error)
    JournalPut(ctx context.Context, j *job.Journal) error
    Record(ctx context.Context, jobID string, rec job.HistoryRecord) error
}

type Request struct {
    ID   int             `json:"id"`
    Op   string          `json:"op"`
    Args json.RawMessage `json:"args"`
}
type Response struct {
    ID     int             `json:"id"`
    OK     bool            `json:"ok"`
    Result json.RawMessage `json:"result,omitempty"`
    Error  *Error          `json:"error,omitempty"`
}
type Error struct {
    Code    string `json:"code"`    // "usage" | "not-found" | "conflict" | "drift" | "unavailable" | "internal"
    Message string `json:"message"`
}
func (e *Error) Error() string

// Local is the in-process implementation used on whichever side is local
// and by Server.
func NewLocal(p session.Paths, selfExe string, opts LocalOptions) *Local
type LocalOptions struct {
    ProcRoot string          // "/proc"
    Probe    session.PaneProbe
    Tmux     tmuxx.Dialer    // Plan 03; nil = tmux unavailable
    Logf     func(string, ...any)
}

// Serve runs the helper: reads Requests from r, writes Responses to w.
func Serve(ctx context.Context, r io.Reader, w io.Writer, ep Endpoint) error
// ServeStream handles `remote stream <kind> <job> <id>`: connects stdin/stdout to the local stream endpoint.
func ServeStream(ctx context.Context, kind StreamKind, jobID, streamID string, stdin io.Reader, stdout io.Writer, ep Endpoint) error

// Client implements Endpoint over an sshx.Client.
func NewClient(ctx context.Context, ssh *sshx.Client, remoteExe string, logf func(string, ...any)) (*Client, error) // runs `<exe> remote serve`, performs Hello
func (c *Client) Close() error
```

## internal/transfer (Plan 02)

```go
package transfer

type Entry struct {
    ID       int              `json:"id"`
    Category session.Category `json:"category"`
    Src      string           `json:"src"`   // absolute on source
    Dst      string           `json:"dst"`   // absolute on destination
    Size     int64            `json:"size"`
    Mode     uint32           `json:"mode"`
    ModTime  time.Time        `json:"mtime"`
    SHA256   string           `json:"sha256"` // "" for dirs/symlinks
    Symlink  string           `json:"symlink,omitempty"`
    Rewrite  bool             `json:"rewrite"`
    FFAllowed bool            `json:"ff_allowed"` // transcript/sidecar of THIS session
}
type Manifest struct {
    Version    int              `json:"version"` // 1
    JobID      string           `json:"job_id"`
    SessionID  string           `json:"session_id"`
    SourceHost string           `json:"source_host"`
    DestHost   string           `json:"dest_host"`
    PathMap    session.PathMap  `json:"path_map"`
    Entries    []Entry          `json:"entries"`
    Skipped    []session.Skipped `json:"skipped"`
}
// Build hashes every file (streaming) and computes Dst via the path map.
func Build(ctx context.Context, jobID string, id session.ID, srcHost, dstHost string, files []session.FileEntry, pm session.PathMap) (*Manifest, error)
func Load(path string) (*Manifest, error)
func (m *Manifest) Save(path string) error
func (m *Manifest) ByID(id int) (Entry, bool)

type Status string
const (
    Absent           Status = "absent"
    PresentSame      Status = "present-same"
    StagedSame       Status = "staged-same"
    PresentDifferent Status = "present-different"
    FFCandidate      Status = "ff-candidate"
    StagedMismatch   Status = "staged-mismatch"
)
// Diff runs on the destination.
func Diff(ctx context.Context, m *Manifest, stagingDir string) (map[int]Status, error)
// Need lists entry ids that must be sent given statuses.
func Need(m *Manifest, st map[int]Status) []int
// Blocking lists entries whose status forbids install (PresentDifferent, or
// FFCandidate without FFAllowed).
func Blocking(m *Manifest, st map[int]Status, force bool) []Entry

// Send writes a gzip'd tar of the needed entries (manifest order) to w.
// Entries with Rewrite=true are streamed through session.RewriteJSONL/JSON
// (the hash in the manifest is of the REWRITTEN content: Build computes it that way).
func Send(ctx context.Context, m *Manifest, need []int, w io.Writer, progress func(Entry, int64)) error
// Receive reads the stream into stagingDir/<id>.part, verifies, renames to stagingDir/<id>.
func Receive(ctx context.Context, m *Manifest, r io.Reader, stagingDir string, progress func(Entry, int64)) error

type InstallReport struct {
    Installed, SkippedSame, FastForwarded int
    IndexMerged, HistoryAdded            int
    ProjectEntryAdded                    bool
    MemoryCopied, MemoryDiffers          []string
}
// Install moves staged entries into place per spec §7.5 and performs the
// merges. Fails (without partial damage beyond files already moved, which
// are all idempotent) on the first conflict.
func Install(ctx context.Context, m *Manifest, st map[int]Status, stagingDir string, p session.Paths, extra InstallExtras) (*InstallReport, error)
type InstallExtras struct {
    IndexEntry   *session.IndexEntry
    History      []json.RawMessage
    ProjectCwd   string
    ProjectEntry session.ProjectEntry
    Memory       []Entry // memory files: copy only if absent
}
```

## internal/job (Plan 02)

```go
package job

type StepStatus string
const (
    Pending StepStatus = "pending"
    Running StepStatus = "running"
    Done    StepStatus = "done"
    Failed  StepStatus = "failed"
)
type StepState struct {
    Name       string     `json:"name"`
    Status     StepStatus `json:"status"`
    StartedAt  time.Time  `json:"started_at,omitempty"`
    FinishedAt time.Time  `json:"finished_at,omitempty"`
    Error      string     `json:"error,omitempty"`
    Attempts   int        `json:"attempts"`
}
type Journal struct {
    ID         string          `json:"id"`          // == session id
    SessionID  string          `json:"session_id"`
    Direction  string          `json:"direction"`   // "to" | "from"
    SourceHost string          `json:"source_host"`
    DestHost   string          `json:"dest_host"`
    CreatedAt  time.Time       `json:"created_at"`
    UpdatedAt  time.Time       `json:"updated_at"`
    Plan       json.RawMessage `json:"plan"`        // orchestrate.Plan (opaque here)
    Steps      []StepState     `json:"steps"`
    Finished   bool            `json:"finished"`
    Outcome    string          `json:"outcome"`     // "" | "success" | "failed" | "abandoned"
    RunnerPID  int             `json:"runner_pid"`
    Dir        string          `json:"-"`
}
func Dir(dataDir, id string) string                   // <dataDir>/jobs/<id>
func StagingDir(dataDir, id string) string            // <dataDir>/staging/<id>
func Open(dataDir, id string) (*Journal, bool, error) // load if exists
func New(dataDir, id string) (*Journal, error)        // creates dir 0700
func (j *Journal) Save() error                        // temp+rename
func (j *Journal) LogPath() string
func (j *Journal) ManifestPath() string
func (j *Journal) CapturePath() string
func (j *Journal) Step(name string) *StepState        // find or append
func (j *Journal) FirstIncomplete() (string, bool)
func (j *Journal) RunnerAlive(alive func(pid int) bool) bool

type Step struct {
    Name   string
    Verify func(ctx context.Context) (done bool, err error) // re-check reality; done=true skips Run
    Run    func(ctx context.Context) error
}
// Run executes steps in order, persisting state before/after each; returns
// the first error (journal marked Failed for that step).
func Run(ctx context.Context, j *Journal, steps []Step, logf func(string, ...any)) error

type HistoryRecord struct {
    At        time.Time `json:"at"`
    SessionID string    `json:"session_id"`
    Direction string    `json:"direction"`
    From, To  string    `json:"from","to"`
    Outcome   string    `json:"outcome"`
    Note      string    `json:"note,omitempty"`
}
func AppendHistory(dir string, r HistoryRecord) error // jobs/<id>/history.jsonl
func TailLog(path string, n int) ([]string, error)
func FollowLog(ctx context.Context, path string, w io.Writer, done func() bool) error
```

## internal/fakeapi (Plan 02)

```go
package fakeapi
type Server struct{ /* ... */ }
type Options struct {
    Reply   string          // canned assistant text
    Model   string          // reported model id
    LogDir  string          // one file per request body, "" = memory only
}
func New(o Options) *Server
func (s *Server) Handler() http.Handler
func (s *Server) Requests() []Request  // recorded bodies (path, body, time)
type Request struct{ Path string; Body []byte; At time.Time }
```

## internal/gitx (Plan 03)

```go
package gitx

type Dirty struct{ Staged, Modified, Untracked, Deleted []string }
type Info struct {
    Root        string   // worktree root (W)
    CommonDir   string   // M/.git
    MainDir     string   // M
    IsLinked    bool
    WorktreeName string  // basename under .git/worktrees
    Branch      string   // "" if detached
    Head        string   // hex
    Detached    bool
    RootCommit  string
    Dirty       Dirty
    Submodules  []string
    OtherWorktrees []string // absolute paths of other linked worktrees
}
var ErrNotRepo = errors.New("not a git repository")
func Inspect(cwd string) (*Info, error)

type DestState struct {
    MainExists     bool
    RootCommit     string
    RefTips        map[string]string // refs/heads/x -> hex
    BranchTip      string            // "" if absent
    WorktreeExists bool
    WorktreeBranch string            // branch checked out at worktreeDir if it exists
    Clean          bool              // for W==M case
    BranchCheckedOutElsewhere string // path, if the branch is checked out in another worktree
}
func DestStateOf(mainDir, worktreeDir, branch string) (*DestState, error)

type Mode string
const (
    ModeNotRepo      Mode = "not-repo"
    ModeFreshMain    Mode = "fresh-main"     // M absent: transfer everything
    ModeExistingMain Mode = "existing-main"  // M present: pack + attach
)
type Plan struct {
    Mode         Mode
    SrcMain, SrcWorktree string
    DstMain, DstWorktree string
    Linked       bool
    WorktreeName string
    Branch       string
    Tip          string
    Detached     bool
    NeedPack     bool
    HaveTips     []string   // destination tips to exclude from the pack
    FastForward  bool       // branch exists on dest and is an ancestor of Tip
    Dirty        Dirty
}
// PlanTransfer decides the mode or returns a *RefuseError (spec §8).
func PlanTransfer(src *Info, dst *DestState, pm session.PathMap) (*Plan, error)
type RefuseError struct{ Reason string }
func (e *RefuseError) Error() string
// Files lists what to move for the plan (repo, worktree categories; excludes
// other worktrees and their metadata; honours exclude globs).
func Files(p *Plan, excludes []string, includeIgnored bool) ([]session.FileEntry, error)
// WritePack encodes the objects reachable from want but not from have.
func WritePack(ctx context.Context, repoDir string, want []string, have []string, w io.Writer) error
// Attach performs the destination side (spec §8): repair metadata for
// fresh-main, or index pack + refs + worktree creation + dirty apply for
// existing-main. packPath may be "".
func Attach(ctx context.Context, p *Plan, packPath string, dirtyFiles map[string]string /* dst path -> staged file */) error
```

## internal/tmuxx (Plan 03)

```go
package tmuxx

// Transport / Quote / Run are copied from go-tmux-saver internal/tmuxctl.
type Transport interface {
    Run(ctx context.Context, cmd string) ([]string, error)
    Close() error
}
func Quote(s string) string
type Dialer func(ctx context.Context, socketPath string) (Transport, error)
func DialControl(ctx context.Context, socketPath string) (Transport, error) // `tmux -S <socket> -C`

type Facts struct {
    SocketPath   string
    SessionName  string
    Group        string   // session_group or "" 
    WindowID     string
    WindowIndex  int
    WindowName   string
    AutoRename   bool
    PaneID       string
    PaneTitle    string
    PaneCwd      string
    PaneCommand  string
    PanePID      int
    HistorySize  int
}
func Describe(ctx context.Context, t Transport, paneID string) (*Facts, error)

// FindServer implements spec §9 discovery. Returns the socket path.
func FindServer(socketDir string, preferredName string, override string) (string, error)
func ListServers(socketDir string) ([]string, error)

type Plan struct {
    SocketPath  string
    Group       string
    WindowName  string
    AutoRename  bool
    Cwd         string
    CreateSession bool  // no session in Group exists
}
type PaneState struct {
    PaneID   string
    Command  string
    Argv     []string
    PID      int
    Content  []string // last 50 lines
}
func OpenWindow(ctx context.Context, t Transport, p *Plan) (*session.TmuxRef, error)
func Capture(ctx context.Context, t Transport, paneID string) ([]byte, error)  // -epJ -S -
func SendKeys(ctx context.Context, t Transport, paneID string, keys ...string) error
func TypeCommand(ctx context.Context, t Transport, paneID string, argv []string) error // leading space + Enter
func State(ctx context.Context, t Transport, paneID string, procs *procx.Table) (*PaneState, error)
func KillWindow(ctx context.Context, t Transport, windowID string) error

// Prober adapts a Transport to session.PaneProbe.
func Prober(ctx context.Context, t Transport, procs *procx.Table, socketPath string) session.PaneProbe
```

## internal/orchestrate (Plan 03)

```go
package orchestrate

type Options struct {
    Direction   string        // "to" | "from"
    Selector    session.Selector
    DestPath    string
    Maps        []session.Mapping
    State       string        // auto|running|suspended|idle
    AllowDrift  bool
    Force       bool
    TmuxSocket  string
    NoTmux      bool
    Excludes    []string
    IncludeIgnored bool
    ExitTimeout, StartTimeout time.Duration
    BangMode    bool          // running inside the session ($CLAUDE_PID == source pid)
}
type Plan struct {
    Options    Options
    Session    *session.Session
    SourceInfo remote.HostInfo
    DestInfo   remote.HostInfo
    PathMap    session.PathMap
    Git        *gitx.Plan
    Tmux       *tmuxx.Plan       // nil = no tmux on dest
    TargetState string           // resolved from auto
    Drift      claudecfg.Report
    ManifestPath string
    Collisions []transfer.Entry
}
func Preflight(ctx context.Context, o Options, src, dst remote.Endpoint, jobID string) (*Plan, error)
func (p *Plan) Render(w io.Writer)
// Steps builds the job.Step list for the plan (spec §6 table).
func Steps(p *Plan, j *job.Journal, src, dst remote.Endpoint, selfExe string, logf func(string, ...any)) []job.Step
```

## internal/cli (Plan 01, extended)

Commands: root (teleport), `continue`, `status`, `abandon`, `inspect`, `list`,
`compare-config`, `doctor`, `placeholder`, `version`, `remote serve|stream`,
`internal-freezer`, `internal-runner`. Exit codes per spec §5:

```go
package cli
const (
    ExitOK          = 0
    ExitFailed      = 1
    ExitUsage       = 2
    ExitRefused     = 3
    ExitUnreachable = 4
    ExitNotResumed  = 5
    ExitInterrupted = 6
)
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer, env []string) int
```
