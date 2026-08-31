package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

// planView is the slice of orchestrate.Plan (stored opaquely in the
// journal) that remote needs. JSON keys match orchestrate.Plan's tags.
type planView struct {
	Statuses map[int]transfer.Status `json:"statuses"`
	Git      *gitx.Plan              `json:"git"`
	Extras   *transfer.InstallExtras `json:"extras"`
}

func (l *Local) planView(jobID string) (*planView, error) {
	j, ok, err := job.Open(l.paths.DataDir, jobID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, &Error{Code: "not-found", Message: fmt.Sprintf("no journal for job %s on this host", jobID)}
	}
	var v planView
	if len(j.Plan) > 0 {
		if err := json.Unmarshal(j.Plan, &v); err != nil {
			return nil, fmt.Errorf("decode plan of job %s: %w", jobID, err)
		}
	}
	return &v, nil
}

// pipeStream adapts a run(r, w) function to io.ReadWriteCloser: bytes
// written go to r; bytes run writes to w come out of Read; Close ends
// the input, fails the output, waits for run and returns its error.
type pipeStream struct {
	inR, outR *io.PipeReader
	inW, outW *io.PipeWriter
	done      chan error
}

// PipeStream runs fn in a goroutine connected to the returned stream.
func PipeStream(fn func(r io.Reader, w io.Writer) error) io.ReadWriteCloser {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &pipeStream{inR: inR, inW: inW, outR: outR, outW: outW, done: make(chan error, 1)}
	go func() {
		err := fn(inR, outW)
		outW.CloseWithError(err)
		inR.CloseWithError(err)
		s.done <- err
	}()
	return s
}

func (s *pipeStream) Read(p []byte) (int, error)  { return s.outR.Read(p) }
func (s *pipeStream) Write(p []byte) (int, error) { return s.inW.Write(p) }

// Close ends the input, then FAILS the output side before waiting for fn.
// The order matters (I3): a consumer that stops reading part-way — which
// is what the driver's io.Copy does the moment the destination errors —
// leaves fn blocked writing into outW, and a bare `<-s.done` would then
// never return. CloseWithError makes fn's next write fail so it unwinds
// and reports; the error fn returns is still what Close returns.
func (s *pipeStream) Close() error {
	s.inW.Close()
	s.outR.CloseWithError(io.ErrClosedPipe)
	return <-s.done
}

// splitStreamID parses "send:<n>" / "recv:<n>".
func splitStreamID(id string) (dir string, err error) {
	dir, _, ok := strings.Cut(id, ":")
	if !ok || (dir != "send" && dir != "recv") {
		return "", &Error{Code: "usage", Message: fmt.Sprintf("stream id %q must be send:<n> or recv:<n>", id)}
	}
	return dir, nil
}

// runStream is the single implementation behind ServeStream and
// Local.OpenStream.
func (l *Local) runStream(ctx context.Context, kind StreamKind, jobID, streamID string, r io.Reader, w io.Writer) error {
	dir, err := splitStreamID(streamID)
	if err != nil {
		return err
	}
	jobDir := l.jobDir(jobID)
	staging := l.stagingDir(jobID)
	switch {
	case kind == StreamTar && dir == "send":
		m, err := transfer.Load(filepath.Join(jobDir, "manifest.json"))
		if err != nil {
			return err
		}
		v, err := l.planView(jobID)
		if err != nil {
			return err
		}
		need := transfer.Need(m, v.Statuses)
		return transfer.Send(ctx, m, need, w, func(e transfer.Entry, n int64) { l.opts.Logf("send %s (%d bytes)", e.Src, n) })
	case kind == StreamTar && dir == "recv":
		m, err := transfer.Load(filepath.Join(jobDir, "manifest.json"))
		if err != nil {
			return err
		}
		if err := os.MkdirAll(staging, 0o700); err != nil {
			return err
		}
		return transfer.Receive(ctx, m, r, staging, func(e transfer.Entry, n int64) { l.opts.Logf("recv %s (%d bytes)", e.Dst, n) })
	case kind == StreamPack && dir == "send":
		v, err := l.planView(jobID)
		if err != nil {
			return err
		}
		if v.Git == nil {
			return &Error{Code: "usage", Message: "pack stream: journal plan has no git plan"}
		}
		want := append([]string{v.Git.Tip}, v.Git.StagedBlobs...)
		return gitx.WritePack(ctx, v.Git.SrcMain, want, v.Git.HaveTips, w)
	case kind == StreamPack && dir == "recv":
		if err := os.MkdirAll(staging, 0o700); err != nil {
			return err
		}
		part := filepath.Join(staging, "objects.pack.part")
		f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, r); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		return os.Rename(part, filepath.Join(staging, "objects.pack"))
	case kind == StreamCapture && dir == "send":
		return copyFileTo(filepath.Join(jobDir, "capture.txt"), w)
	case kind == StreamLog && dir == "send":
		return copyFileTo(filepath.Join(jobDir, "log.txt"), w)
	}
	return &Error{Code: "usage", Message: fmt.Sprintf("unsupported stream %s %s", kind, streamID)}
}

func copyFileTo(path string, w io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// OpenStream (Local) runs the stream in-process.
func (l *Local) OpenStream(ctx context.Context, kind StreamKind, jobID, streamID string) (io.ReadWriteCloser, error) {
	if _, err := splitStreamID(streamID); err != nil {
		return nil, err
	}
	return PipeStream(func(r io.Reader, w io.Writer) error { return l.runStream(ctx, kind, jobID, streamID, r, w) }), nil
}

// ServeStream handles `remote stream <kind> <job> <id>`. streamID carries
// the direction (send:<n> / recv:<n>), so runStream already knows whether
// it needs stdin, stdout, or neither — unlike Task 15's generic pump, there
// is no concurrent bidirectional copy and no half-close dance to document.
func ServeStream(ctx context.Context, kind StreamKind, jobID, streamID string, stdin io.Reader, stdout io.Writer, ep Endpoint) error {
	l, ok := ep.(*Local)
	if !ok {
		return &Error{Code: "internal", Message: "ServeStream needs a *Local endpoint"}
	}
	return l.runStream(ctx, kind, jobID, streamID, stdin, stdout)
}
