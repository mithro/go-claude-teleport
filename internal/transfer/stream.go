package transfer

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
)

// Send writes a gzip'd tar of the needed entries (manifest order) to w.
// Rewrite entries are rewritten to a temp file first: tar needs the exact
// size in the header before the body, and the rewrite changes the size.
func Send(ctx context.Context, m *Manifest, need []int, w io.Writer, progress func(Entry, int64)) error {
	wanted := make(map[int]bool, len(need))
	for _, id := range need {
		wanted[id] = true
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	var total int64
	for _, e := range m.Entries {
		if !wanted[e.ID] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr := &tar.Header{Name: strconv.Itoa(e.ID), Mode: int64(os.FileMode(e.Mode).Perm()), ModTime: e.ModTime}
		switch {
		case e.IsDir():
			hdr.Typeflag = tar.TypeDir
			if err := tw.WriteHeader(hdr); err != nil {
				return fmt.Errorf("send entry %d (%s): %w", e.ID, e.Src, err)
			}
		case e.IsSymlink():
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = e.Symlink
			if err := tw.WriteHeader(hdr); err != nil {
				return fmt.Errorf("send entry %d (%s): %w", e.ID, e.Src, err)
			}
		default:
			hdr.Typeflag = tar.TypeReg
			hdr.Size = e.Size
			if err := sendFile(tw, hdr, e, m); err != nil {
				return err
			}
		}
		total += e.Size
		if progress != nil {
			progress(e, total)
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("send: close tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("send: close gzip: %w", err)
	}
	return nil
}

// sendFile writes one regular file's header and body. It routes bytes
// through the SAME copyMaybeRewritten decision Build used to compute the
// manifest hash (hashEntry calls it too), so a rewritten entry's stream can
// never disagree with its hash: both call sites pass the same (rewrite, pm)
// pair, and copyMaybeRewritten alone decides whether bytes are rewritten.
func sendFile(tw *tar.Writer, hdr *tar.Header, e Entry, m *Manifest) error {
	src, err := os.Open(e.Src)
	if err != nil {
		return fmt.Errorf("send entry %d: %w", e.ID, err)
	}
	defer src.Close()
	var body io.Reader = src
	if e.Rewrite && !m.PathMap.Empty() {
		tmpDir := m.TmpDir
		if tmpDir == "" {
			tmpDir = os.TempDir()
		}
		tmp, err := os.CreateTemp(tmpDir, "rewrite-*.tmp")
		if err != nil {
			return fmt.Errorf("send entry %d: temp file: %w", e.ID, err)
		}
		defer os.Remove(tmp.Name())
		defer tmp.Close()
		if err := copyMaybeRewritten(src, tmp, e.Src, e.Rewrite, m.PathMap); err != nil {
			// A rewrite that now fails to parse (as opposed to a clean size
			// change, handled below) still means the source no longer
			// matches what Build validated: report it the same way.
			return fmt.Errorf("send entry %d (%s): source changed since manifest was built: %w", e.ID, e.Src, err)
		}
		st, err := tmp.Stat()
		if err != nil {
			return err
		}
		if st.Size() != e.Size {
			return fmt.Errorf("send entry %d (%s): rewritten size %d != manifest %d: source changed since manifest was built", e.ID, e.Src, st.Size(), e.Size)
		}
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return err
		}
		body = tmp
	} else {
		st, err := src.Stat()
		if err != nil {
			return err
		}
		if st.Size() != e.Size {
			return fmt.Errorf("send entry %d (%s): size %d != manifest %d: source changed since manifest was built", e.ID, e.Src, st.Size(), e.Size)
		}
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("send entry %d (%s): %w", e.ID, e.Src, err)
	}
	if _, err := io.CopyN(tw, body, e.Size); err != nil {
		return fmt.Errorf("send entry %d (%s): body: %w", e.ID, e.Src, err)
	}
	return nil
}

// Receive reads the stream into stagingDir/<id>.part, verifies, renames to
// stagingDir/<id>. A truncated stream loses only the entry in flight.
func Receive(ctx context.Context, m *Manifest, r io.Reader, stagingDir string, progress func(Entry, int64)) error {
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return fmt.Errorf("staging dir %s: %w", stagingDir, err)
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("receive: gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("receive: tar header: %w", err)
		}
		id, err := strconv.Atoi(hdr.Name)
		if err != nil {
			return fmt.Errorf("receive: bad entry name %q", hdr.Name)
		}
		e, ok := m.ByID(id)
		if !ok {
			return fmt.Errorf("receive: entry %d not in manifest", id)
		}
		base := StagedPath(stagingDir, e.ID)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.WriteFile(base+".dir", nil, 0o600); err != nil {
				return fmt.Errorf("receive entry %d: %w", e.ID, err)
			}
		case tar.TypeSymlink:
			if err := os.WriteFile(base+".symlink", []byte(hdr.Linkname), 0o600); err != nil {
				return fmt.Errorf("receive entry %d: %w", e.ID, err)
			}
		case tar.TypeReg:
			if st, serr := os.Stat(base); serr == nil && st.Size() == e.Size {
				if _, err := io.Copy(io.Discard, tr); err != nil {
					return fmt.Errorf("receive entry %d: drain: %w", e.ID, err)
				}
				break
			}
			if err := receiveFile(tr, base, e, hdr.Size); err != nil {
				return err
			}
		default:
			return fmt.Errorf("receive entry %d: unsupported tar type %q", e.ID, hdr.Typeflag)
		}
		total += e.Size
		if progress != nil {
			progress(e, total)
		}
	}
}

func receiveFile(tr *tar.Reader, base string, e Entry, size int64) error {
	part := base + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("receive entry %d: %w", e.ID, err)
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), tr)
	closeErr := f.Close()
	fail := func(err error) error {
		os.Remove(part)
		return err
	}
	if copyErr != nil {
		return fail(fmt.Errorf("receive entry %d (%s): %w", e.ID, e.Dst, copyErr))
	}
	if closeErr != nil {
		return fail(fmt.Errorf("receive entry %d: %w", e.ID, closeErr))
	}
	if n != e.Size || n != size {
		return fail(fmt.Errorf("receive entry %d (%s): size %d, manifest %d", e.ID, e.Dst, n, e.Size))
	}
	if sum := hex.EncodeToString(h.Sum(nil)); sum != e.SHA256 {
		return fail(fmt.Errorf("receive entry %d (%s): sha256 mismatch %s != %s", e.ID, e.Dst, sum, e.SHA256))
	}
	if err := os.Rename(part, base); err != nil {
		return fail(fmt.Errorf("receive entry %d: %w", e.ID, err))
	}
	return nil
}
