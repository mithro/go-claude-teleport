package sshx

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mithro/go-claude-teleport/internal/sshx/sshtest"
)

func TestDialThroughJumpResolvesOnJump(t *testing.T) {
	home, pub := testHome(t)
	dest := sshtest.New(t, sshtest.Options{Authorized: []ssh.PublicKey{pub}, Exec: echoExec})
	jump := sshtest.New(t, sshtest.Options{
		Authorized: []ssh.PublicKey{pub},
		Resolver:   map[string]string{"dest.private": dest.Addr},
	})
	jumpHost, jumpPort := hostPort(t, jump.Addr)

	kh := filepath.Join(home, ".ssh", "known_hosts")
	os.WriteFile(kh, []byte(
		sshtest.KnownHostsLine(knownHostsName(jumpHost, jumpPort), jump.HostKey)+
			sshtest.KnownHostsLine("[dest.private]:2222", dest.HostKey)), 0o600)

	var mu sync.Mutex
	var localDials []string
	recorder := func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		localDials = append(localDials, addr)
		mu.Unlock()
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}

	r := Resolved{
		Target:   Target{User: "alice", Host: "big-storage", Port: 2222, Via: []Target{{User: "alice", Host: jumpHost, Port: jumpPort}}},
		HostName: "dest.private",
	}
	c, err := Dial(context.Background(), r, nil, nil, Options{KnownHostsFile: kh, Home: home, Logf: t.Logf, NetDial: recorder})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if c.String() != "alice@big-storage (via "+jumpHost+")" {
		t.Errorf("String = %q", c.String())
	}
	out, _, err := c.Run(context.Background(), "hostname", nil)
	if err != nil || string(out) != "cmd=hostname stdin=" {
		t.Fatalf("Run via jump: out=%q err=%v", out, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(localDials) != 1 || localDials[0] != jump.Addr {
		t.Errorf("local dials = %v, want only the jump %s", localDials, jump.Addr)
	}
	for _, d := range localDials {
		if strings.Contains(d, "dest.private") {
			t.Errorf("dest.private was dialled locally: %v", localDials)
		}
	}
	if fw := jump.Forwarded(); len(fw) != 1 || fw[0] != "dest.private:2222" {
		t.Errorf("jump forwarded = %v, want [dest.private:2222]", fw)
	}
}

func TestRedialBoundedRetries(t *testing.T) {
	calls := 0
	_, err := Redial(context.Background(), 3, time.Millisecond, t.Logf, func(ctx context.Context) (*Client, error) {
		calls++
		return nil, errors.New("refused")
	})
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
	if err == nil || strings.Count(err.Error(), "refused") != 3 {
		t.Errorf("err = %v, want all three attempts listed", err)
	}

	calls = 0
	c, err := Redial(context.Background(), 3, time.Millisecond, t.Logf, func(ctx context.Context) (*Client, error) {
		calls++
		if calls < 2 {
			return nil, errors.New("refused")
		}
		return &Client{desc: "ok"}, nil
	})
	if err != nil || c == nil || calls != 2 {
		t.Errorf("second attempt should succeed: c=%v err=%v calls=%d", c, err, calls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Redial(ctx, 3, time.Second, t.Logf, func(ctx context.Context) (*Client, error) { return nil, errors.New("x") })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled ctx: err = %v", err)
	}
}
