package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"sync"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// maxLine bounds one request/response line (manifests can be large).
const maxLine = 256 << 20

type handler func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error)

func decode[T any](args json.RawMessage) (T, error) {
	var v T
	if len(args) == 0 {
		return v, nil
	}
	if err := json.Unmarshal(args, &v); err != nil {
		return v, &Error{Code: "usage", Message: "bad args: " + err.Error()}
	}
	return v, nil
}

// dispatch is the op-name -> handler table. Every op's args/result types
// live in ops.go so Client and Server cannot drift apart.
var dispatch = map[string]handler{
	OpHello: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[HelloArgs](args)
		if err != nil {
			return nil, err
		}
		info, err := ep.Hello(ctx)
		if err != nil {
			return nil, err
		}
		if a.Protocol != info.Protocol {
			return nil, &Error{Code: "usage", Message: fmt.Sprintf(
				"protocol mismatch: driver speaks protocol %d (claude-teleport %s), this host speaks protocol %d (claude-teleport %s); install the same version on both hosts",
				a.Protocol, a.Version, info.Protocol, info.Version)}
		}
		return info, nil
	},
	OpPaths: func(ctx context.Context, ep Endpoint, _ json.RawMessage) (any, error) {
		return PathsResult{Paths: ep.Paths()}, nil
	},
	OpResolveSession: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[ResolveSessionArgs](args)
		if err != nil {
			return nil, err
		}
		s, err := ep.ResolveSession(ctx, a.Selector)
		return ResolveSessionResult{Session: s}, err
	},
	OpInventorySession: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[InventorySessionArgs](args)
		if err != nil {
			return nil, err
		}
		inv, usage, err := ep.InventorySession(ctx, a.ID)
		return InventorySessionResult{Inventory: inv, Usage: usage}, err
	},
	OpInventoryHost: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[InventoryHostArgs](args)
		if err != nil {
			return nil, err
		}
		inv, err := ep.InventoryHost(ctx, a.Cwd, a.ClaudeVersion)
		return InventoryHostResult{Inventory: inv}, err
	},
	OpInventoryGit: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[InventoryGitArgs](args)
		if err != nil {
			return nil, err
		}
		info, err := ep.InventoryGit(ctx, a.Cwd)
		return InventoryGitResult{Info: info}, err
	},
	OpGitDestState: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[GitDestStateArgs](args)
		if err != nil {
			return nil, err
		}
		st, err := ep.GitDestState(ctx, a.MainDir, a.WorktreeDir, a.Branch)
		return GitDestStateResult{State: st}, err
	},
	OpInventoryTmux: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[InventoryTmuxArgs](args)
		if err != nil {
			return nil, err
		}
		f, err := ep.InventoryTmux(ctx, a.Ref, a.PreferredSocket)
		return InventoryTmuxResult{Facts: f}, err
	},
	OpManifestDiff: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[ManifestDiffArgs](args)
		if err != nil {
			return nil, err
		}
		st, err := ep.ManifestDiff(ctx, a.Manifest, a.JobID)
		return ManifestDiffResult{Statuses: st}, err
	},
	OpInstallExtras: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[InstallExtrasArgs](args)
		if err != nil {
			return nil, err
		}
		return Empty{}, ep.PutInstallExtras(ctx, a.JobID, a.Extra)
	},
	OpInstall: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[InstallArgs](args)
		if err != nil {
			return nil, err
		}
		rep, err := ep.Install(ctx, a.Manifest, a.JobID)
		return InstallResult{Report: rep}, err
	},
	OpGitAttach: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[GitAttachArgs](args)
		if err != nil {
			return nil, err
		}
		return Empty{}, ep.GitAttach(ctx, a.Plan, a.JobID)
	},
	OpFreeze: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[FreezeArgs](args)
		if err != nil {
			return nil, err
		}
		return Empty{}, ep.Freeze(ctx, a.PID, a.StartTime)
	},
	OpThaw: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[ThawArgs](args)
		if err != nil {
			return nil, err
		}
		return Empty{}, ep.Thaw(ctx, a.PID)
	},
	OpCapture: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[CaptureArgs](args)
		if err != nil {
			return nil, err
		}
		return Empty{}, ep.Capture(ctx, a.Ref, a.JobID)
	},
	OpOpenWindow: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[OpenWindowArgs](args)
		if err != nil {
			return nil, err
		}
		ref, err := ep.OpenWindow(ctx, a.Plan)
		return OpenWindowResult{Ref: ref}, err
	},
	OpStartClaude: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[StartClaudeArgs](args)
		if err != nil {
			return nil, err
		}
		return Empty{}, ep.StartClaude(ctx, a.Ref, a.ID, a.JobID, a.Argv)
	},
	OpConfirmClaude: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[ConfirmClaudeArgs](args)
		if err != nil {
			return nil, err
		}
		reg, err := ep.ConfirmClaude(ctx, a.Ref, a.ID, a.Timeout)
		return ConfirmClaudeResult{Registry: reg}, err
	},
	OpExitClaude: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[ExitClaudeArgs](args)
		if err != nil {
			return nil, err
		}
		return Empty{}, ep.ExitClaude(ctx, a.Ref, a.PID, a.StartTime, a.Timeout)
	},
	OpTypeCommand: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[TypeCommandArgs](args)
		if err != nil {
			return nil, err
		}
		return Empty{}, ep.TypeCommand(ctx, a.Ref, a.Argv)
	},
	OpPaneState: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[PaneStateArgs](args)
		if err != nil {
			return nil, err
		}
		st, err := ep.PaneState(ctx, a.Ref)
		return PaneStateResult{State: st}, err
	},
	OpRunPtyResume: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[RunPtyResumeArgs](args)
		if err != nil {
			return nil, err
		}
		return Empty{}, ep.RunPtyResume(ctx, a.ID, a.Cwd, a.Timeout)
	},
	OpJournalGet: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[JournalGetArgs](args)
		if err != nil {
			return nil, err
		}
		j, found, err := ep.JournalGet(ctx, a.JobID)
		return JournalGetResult{Journal: j, Found: found}, err
	},
	OpJournalPut: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[JournalPutArgs](args)
		if err != nil {
			return nil, err
		}
		return Empty{}, ep.JournalPut(ctx, a.Journal)
	},
	OpRecord: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[RecordArgs](args)
		if err != nil {
			return nil, err
		}
		return Empty{}, ep.Record(ctx, a.JobID, a.Record)
	},
}

// toError maps any error to a protocol Error.
func toError(err error) *Error {
	var pe *Error
	if errors.As(err, &pe) {
		return pe
	}
	if errors.Is(err, session.ErrNotFound) {
		return &Error{Code: "not-found", Message: err.Error()}
	}
	return &Error{Code: "internal", Message: err.Error()}
}

func handle(ctx context.Context, ep Endpoint, req Request) (resp Response) {
	resp = Response{ID: req.ID}
	defer func() {
		if r := recover(); r != nil {
			resp.OK = false
			resp.Result = nil
			resp.Error = &Error{Code: "internal", Message: fmt.Sprintf("panic in %s: %v\n%s", req.Op, r, debug.Stack())}
		}
	}()
	h, ok := dispatch[req.Op]
	if !ok {
		resp.Error = &Error{Code: "usage", Message: "unknown op " + req.Op}
		return resp
	}
	result, err := h(ctx, ep, req.Args)
	if err != nil {
		resp.Error = toError(err)
		return resp
	}
	raw, err := json.Marshal(result)
	if err != nil {
		resp.Error = &Error{Code: "internal", Message: "encode result: " + err.Error()}
		return resp
	}
	resp.OK = true
	resp.Result = raw
	return resp
}

// Serve runs the helper: reads Requests from r (one per line), handles them
// one at a time, writes Responses to w. Returns nil at EOF.
func Serve(ctx context.Context, r io.Reader, w io.Writer, ep Endpoint) error {
	var wmu sync.Mutex
	write := func(resp Response) error {
		raw, err := json.Marshal(resp)
		if err != nil {
			return err
		}
		wmu.Lock()
		defer wmu.Unlock()
		_, err = w.Write(append(raw, '\n'))
		return err
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), maxLine)
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			if werr := write(Response{Error: &Error{Code: "usage", Message: "bad request line: " + err.Error()}}); werr != nil {
				return werr
			}
			continue
		}
		if err := write(handle(ctx, ep, req)); err != nil {
			return fmt.Errorf("write response %d: %w", req.ID, err)
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		return fmt.Errorf("read requests: %w", err)
	}
	return nil
}

// ServeStream handles `remote stream <kind> <job> <id>`: connects stdin/stdout
// to the local stream endpoint returned by ep.OpenStream. Bytes read from
// stdin are copied INTO the stream; bytes read from the stream are copied to
// stdout. The two directions run concurrently in separate goroutines, and
// the stream is only Close()d once BOTH have finished.
//
// Half-close contract (spec deadlock/truncation fix): the client signals
// "no more inbound data" by closing (or half-closing the write side of) its
// stdin; ServeStream treats that stdin EOF as end-of-inbound for every
// StreamKind, including StreamTar where the client is actively sending a
// large payload. For the receive-direction kinds (StreamCapture, StreamPack,
// StreamLog: this host produces data, the client only reads) the client has
// nothing to write, so Task 16's client half-closes stdin *before* it starts
// reading stdout — if it instead waited to close stdin until after reading,
// both sides would block forever (server stuck in the inbound copy waiting
// for stdin EOF; client stuck reading stdout waiting for data the server
// hasn't started sending). Because ServeStream waits for BOTH copies before
// calling Close, that early inbound EOF can never truncate an in-flight
// outbound copy — closing the stream only happens once whichever direction
// actually carries the payload has fully drained.
func ServeStream(ctx context.Context, kind StreamKind, jobID, streamID string, stdin io.Reader, stdout io.Writer, ep Endpoint) error {
	s, err := ep.OpenStream(ctx, kind, jobID, streamID)
	if err != nil {
		return err
	}
	inDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(s, stdin)
		inDone <- err
	}()
	outDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(stdout, s)
		outDone <- err
	}()
	inErr := <-inDone
	outErr := <-outDone
	closeErr := s.Close()
	if inErr != nil {
		return fmt.Errorf("stream %s/%s: stdin: %w", kind, streamID, inErr)
	}
	if outErr != nil {
		return fmt.Errorf("stream %s/%s: stdout: %w", kind, streamID, outErr)
	}
	if closeErr != nil {
		return fmt.Errorf("stream %s/%s: %w", kind, streamID, closeErr)
	}
	return nil
}
