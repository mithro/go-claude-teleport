package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// RewriteStats reports what a rewrite touched.
type RewriteStats struct{ Records, Rewritten, Unparseable int }

// rewriteValue walks a decoded JSON value, rewriting every string (values
// AND object keys — file-history snapshots key on absolute paths) and
// reports whether anything changed. Numbers are json.Number (UseNumber) and
// pass through untouched.
func rewriteValue(v any, m PathMap) (any, bool) {
	switch x := v.(type) {
	case string:
		n := m.Apply(x)
		return n, n != x
	case map[string]any:
		out := make(map[string]any, len(x))
		changed := false
		for k, val := range x {
			nk := m.Apply(k)        // Note: two distinct keys may rewrite to the same key; later entry wins.
			nv, c := rewriteValue(val, m)
			if nk != k || c {
				changed = true
			}
			out[nk] = nv
		}
		return out, changed
	case []any:
		changed := false
		for i, val := range x {
			nv, c := rewriteValue(val, m)
			x[i] = nv
			changed = changed || c
		}
		return x, changed
	default:
		return v, false
	}
}

func decodeOne(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing data after JSON value")
	}
	return v, nil
}

func encodeCompact(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v) // appends "\n"
}

// RewriteJSONL streams r to w line by line. Each parseable line is decoded
// (UseNumber), rewritten and re-encoded compactly with SetEscapeHTML(false);
// unparseable lines are copied verbatim and counted; blank lines stay blank.
// Every output line ends with "\n".
func RewriteJSONL(r io.Reader, w io.Writer, m PathMap) (RewriteStats, error) {
	var st RewriteStats
	br := bufio.NewReaderSize(r, 1<<20)
	bw := bufio.NewWriterSize(w, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) == 0 && err == io.EOF {
			break
		}
		if err != nil && err != io.EOF {
			return st, fmt.Errorf("rewrite jsonl: read: %w", err)
		}
		body := bytes.TrimRight(line, "\r\n")
		if len(bytes.TrimSpace(body)) == 0 {
			bw.WriteString("\n")
		} else {
			st.Records++
			if v, perr := decodeOne(body); perr != nil {
				st.Unparseable++
				bw.Write(body)
				bw.WriteString("\n")
			} else {
				nv, changed := rewriteValue(v, m)
				if changed {
					st.Rewritten++
				}
				if err := encodeCompact(bw, nv); err != nil {
					return st, fmt.Errorf("rewrite jsonl: encode record %d: %w", st.Records, err)
				}
			}
		}
		if err == io.EOF {
			break
		}
	}
	if err := bw.Flush(); err != nil {
		return st, fmt.Errorf("rewrite jsonl: write: %w", err)
	}
	return st, nil
}

// RewriteJSON rewrites a single JSON document, re-encoded with 2-space
// indentation (as Claude Code writes its .json files).
func RewriteJSON(r io.Reader, w io.Writer, m PathMap) (RewriteStats, error) {
	var st RewriteStats
	data, err := io.ReadAll(r)
	if err != nil {
		return st, fmt.Errorf("rewrite json: read: %w", err)
	}
	v, err := decodeOne(data)
	if err != nil {
		return st, fmt.Errorf("rewrite json: parse: %w", err)
	}
	st.Records = 1
	nv, changed := rewriteValue(v, m)
	if changed {
		st.Rewritten = 1
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(nv); err != nil {
		return st, fmt.Errorf("rewrite json: encode: %w", err)
	}
	return st, nil
}
