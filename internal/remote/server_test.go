package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/version"
)

// stubEndpoint embeds a nil Endpoint so only the methods we override exist.
type stubEndpoint struct {
	Endpoint
	hello func() (HostInfo, error)
}

func (s stubEndpoint) Hello(ctx context.Context) (HostInfo, error) { return s.hello() }
func (s stubEndpoint) Paths() session.Paths                        { return session.Paths{Home: "/home/alice"} }
func (s stubEndpoint) Thaw(ctx context.Context, pid int) error {
	if pid == 0 {
		panic("pid zero")
	}
	return &Error{Code: "not-found", Message: "no such pid"}
}

func roundTrip(t *testing.T, ep Endpoint, reqs ...string) []Response {
	t.Helper()
	in := strings.NewReader(strings.Join(reqs, "\n") + "\n")
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), in, pw, ep); pw.Close() }()
	var out []Response
	sc := bufio.NewScanner(pr)
	for sc.Scan() {
		var r Response
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("bad response line %q: %v", sc.Text(), err)
		}
		out = append(out, r)
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	return out
}

func TestServeHelloAndProtocolMismatch(t *testing.T) {
	ep := stubEndpoint{hello: func() (HostInfo, error) {
		return HostInfo{Version: version.Version, Protocol: version.Protocol, Hostname: "big-storage.example"}, nil
	}}
	rs := roundTrip(t, ep,
		`{"id":1,"op":"hello","args":{"version":"v0.3","protocol":1}}`,
		`{"id":2,"op":"hello","args":{"version":"v0.3","protocol":99}}`,
		`{"id":3,"op":"paths","args":{}}`,
	)
	if len(rs) != 3 {
		t.Fatalf("got %d responses", len(rs))
	}
	if !rs[0].OK || rs[0].ID != 1 || !strings.Contains(string(rs[0].Result), `"hostname":"big-storage.example"`) {
		t.Errorf("hello: %+v %s", rs[0], rs[0].Result)
	}
	if rs[1].OK || rs[1].Error == nil || rs[1].Error.Code != "usage" || !strings.Contains(rs[1].Error.Message, "99") || !strings.Contains(rs[1].Error.Message, "1") {
		t.Errorf("protocol mismatch must report both versions: %+v", rs[1].Error)
	}
	var pr PathsResult
	json.Unmarshal(rs[2].Result, &pr)
	if pr.Paths.Home != "/home/alice" {
		t.Errorf("paths: %+v", pr)
	}
}

func TestServeErrorsAndPanics(t *testing.T) {
	ep := stubEndpoint{hello: func() (HostInfo, error) { return HostInfo{}, errors.New("nope") }}
	rs := roundTrip(t, ep,
		`{"id":1,"op":"thaw","args":{"pid":42}}`,
		`{"id":2,"op":"thaw","args":{"pid":0}}`,
		`{"id":3,"op":"no-such-op","args":{}}`,
		`not json at all`,
		`{"id":5,"op":"thaw","args":{"pid":"x"}}`,
	)
	want := []struct {
		id   int
		code string
	}{{1, "not-found"}, {2, "internal"}, {3, "usage"}, {0, "usage"}, {5, "usage"}}
	if len(rs) != len(want) {
		t.Fatalf("got %d responses: %+v", len(rs), rs)
	}
	for i, w := range want {
		if rs[i].OK || rs[i].ID != w.id || rs[i].Error == nil || rs[i].Error.Code != w.code {
			t.Errorf("response %d = %+v (err %+v), want id=%d code=%s", i, rs[i], rs[i].Error, w.id, w.code)
		}
	}
	if !strings.Contains(rs[1].Error.Message, "pid zero") {
		t.Errorf("panic text must be reported: %+v", rs[1].Error)
	}
}

func TestServeOverNetPipeStopsOnClose(t *testing.T) {
	a, b := net.Pipe()
	ep := stubEndpoint{hello: func() (HostInfo, error) { return HostInfo{Protocol: version.Protocol}, nil }}
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), a, a, ep) }()
	io.WriteString(b, `{"id":1,"op":"hello","args":{"protocol":1}}`+"\n")
	line, _ := bufio.NewReader(b).ReadString('\n')
	if !strings.Contains(line, `"ok":true`) {
		t.Errorf("line = %q", line)
	}
	b.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve after peer close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after the peer closed")
	}
}
