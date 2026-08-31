package session

import (
	"bytes"
	"fmt"
	"io"
	"os"
)

// IsPrefix reports whether file `existing` is a byte-prefix of `incoming`
// (streaming; spec §7.3 fast-forward rule). Equal files are a prefix.
func IsPrefix(existing, incoming string) (bool, error) {
	ef, err := os.Open(existing)
	if err != nil {
		return false, fmt.Errorf("is-prefix: %w", err)
	}
	defer ef.Close()
	inf, err := os.Open(incoming)
	if err != nil {
		return false, fmt.Errorf("is-prefix: %w", err)
	}
	defer inf.Close()
	es, err := ef.Stat()
	if err != nil {
		return false, err
	}
	is, err := inf.Stat()
	if err != nil {
		return false, err
	}
	if es.Size() > is.Size() {
		return false, nil
	}
	eb, ib := make([]byte, 64*1024), make([]byte, 64*1024)
	for {
		n, eerr := io.ReadFull(ef, eb)
		if n == 0 && (eerr == io.EOF || eerr == io.ErrUnexpectedEOF) {
			return true, nil
		}
		if eerr != nil && eerr != io.EOF && eerr != io.ErrUnexpectedEOF {
			return false, fmt.Errorf("is-prefix: read %s: %w", existing, eerr)
		}
		if _, err := io.ReadFull(inf, ib[:n]); err != nil {
			return false, fmt.Errorf("is-prefix: read %s: %w", incoming, err)
		}
		if !bytes.Equal(eb[:n], ib[:n]) {
			return false, nil
		}
		if eerr == io.EOF || eerr == io.ErrUnexpectedEOF {
			return true, nil
		}
	}
}
