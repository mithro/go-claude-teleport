package session

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"reflect"
)

// IsRecordPrefix reports whether every JSONL record in `existing` is, in
// order, the same record as in `incoming` under JSON semantic equality
// (decoded with UseNumber, then deep-equal) — i.e. existing's records are a
// record-wise prefix of incoming's, tolerating re-encoding (key order,
// whitespace) between the two files. existing having strictly more records
// than incoming, or any record differing, is false. A line that fails to
// parse on either side falls back to an exact byte comparison for that one
// line pair, so two byte-identical unparseable lines still count as equal.
func IsRecordPrefix(existing, incoming string) (bool, error) {
	inf, err := os.Open(incoming)
	if err != nil {
		return false, fmt.Errorf("is-record-prefix: %w", err)
	}
	defer inf.Close()
	is := bufio.NewScanner(inf)
	is.Buffer(make([]byte, 0, 64*1024), scannerBuf)

	prefix := true
	err = scanLines(existing, func(eLine []byte) error {
		iLine, ok, err := nextNonBlankLine(is)
		if err != nil {
			return fmt.Errorf("is-record-prefix: scan %s: %w", incoming, err)
		}
		if !ok {
			prefix = false
			return errStopScan
		}
		eq, err := recordsEqual(eLine, iLine)
		if err != nil {
			return fmt.Errorf("is-record-prefix: %w", err)
		}
		if !eq {
			prefix = false
			return errStopScan
		}
		return nil
	})
	if err != nil && err != errStopScan {
		return false, err
	}
	return prefix, nil
}

// errStopScan is an internal sentinel used to end scanLines early once
// IsRecordPrefix already knows the answer; it never escapes IsRecordPrefix.
var errStopScan = errors.New("is-record-prefix: stop")

// nextNonBlankLine advances sc past blank lines and returns the next
// non-blank one; ok is false at EOF.
func nextNonBlankLine(sc *bufio.Scanner) ([]byte, bool, error) {
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		out := make([]byte, len(line))
		copy(out, line)
		return out, true, nil
	}
	return nil, false, sc.Err()
}

// recordsEqual compares two JSONL lines by JSON semantic equality (UseNumber
// decode, then deep-equal); if either side fails to parse, it falls back to
// an exact byte comparison of the two lines.
func recordsEqual(a, b []byte) (bool, error) {
	av, aErr := decodeOne(a)
	if aErr != nil {
		return bytes.Equal(a, b), nil
	}
	bv, bErr := decodeOne(b)
	if bErr != nil {
		return bytes.Equal(a, b), nil
	}
	return reflect.DeepEqual(av, bv), nil
}
