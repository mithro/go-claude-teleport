package sshtest

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// ExecFunc handles one exec request and returns the exit status.
type ExecFunc func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int

// Options configures a test server.
type Options struct {
	Authorized []ssh.PublicKey   // keys accepted for any user
	Exec       ExecFunc          // nil = every exec exits 127 with "exec not configured"
	Resolver   map[string]string // name -> "127.0.0.1:port" for direct-tcpip; nil = refuse all
	// HostKeys, when set, replaces the single generated ed25519 host key
	// with this list — e.g. an ed25519 signer plus GenECDSAKey's ecdsa
	// signer, so a client's negotiated host-key algorithm can be observed
	// (HK-1: known_hosts host-key-algorithm preference). nil (the default)
	// keeps the original single-key behaviour.
	HostKeys []ssh.Signer
	// SilentGlobalRequests reads global requests (keepalives) and never
	// answers them: a server that is up but has stopped responding.
	SilentGlobalRequests bool
	// GlobalRequestDelay, when set, is consulted for each global request
	// in arrival order (n starts at 1) and the server waits that long
	// before answering it: a host that hiccups and then recovers, as
	// opposed to SilentGlobalRequests' host that never comes back.
	GlobalRequestDelay func(n int) time.Duration
	Logf               func(string, ...any)
}

// Server is a running in-process ssh server bound to 127.0.0.1.
type Server struct {
	Addr    string
	HostKey ssh.PublicKey // first host key (back-compat: every existing test has exactly one)

	// HostKeys lists every host key the server offers, same order as
	// Options.HostKeys (or a single generated ed25519 key when unset).
	HostKeys []ssh.PublicKey

	ln     net.Listener
	config *ssh.ServerConfig
	opts   Options
	mu     sync.Mutex
	fwd    []string
	execs  []string
	users  []string
	wg     sync.WaitGroup
}

// New starts a server; it is closed by t.Cleanup.
func New(t testing.TB, o Options) *Server {
	t.Helper()
	signers := o.HostKeys
	if len(signers) == 0 {
		hostSigner, _ := GenKey(t)
		signers = []ssh.Signer{hostSigner}
	}
	if o.Logf == nil {
		o.Logf = t.Logf
	}
	s := &Server{opts: o}
	s.config = &ssh.ServerConfig{
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			for _, k := range o.Authorized {
				if bytes.Equal(k.Marshal(), key.Marshal()) {
					s.mu.Lock()
					s.users = append(s.users, conn.User())
					s.mu.Unlock()
					return &ssh.Permissions{}, nil
				}
			}
			return nil, fmt.Errorf("sshtest: key not authorized for %q", conn.User())
		},
	}
	for _, signer := range signers {
		s.config.AddHostKey(signer)
		s.HostKeys = append(s.HostKeys, signer.PublicKey())
	}
	s.HostKey = s.HostKeys[0]
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.ln = ln
	s.Addr = ln.Addr().String()
	s.wg.Add(1)
	go s.acceptLoop()
	t.Cleanup(s.Close)
	return s
}

// Close stops accepting; in-flight handlers are abandoned.
func (s *Server) Close() { s.ln.Close() }

// Forwarded lists "host:port" strings requested via direct-tcpip.
func (s *Server) Forwarded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.fwd...)
}

// Execs lists the exec command lines received.
func (s *Server) Execs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.execs...)
}

// Users lists the ssh usernames (conn.User()) that authenticated
// successfully, in order (may contain duplicates: the client library
// probes a key before signing with it, so one handshake can record the
// same user twice).
func (s *Server) Users() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.users...)
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handleConn(c)
	}
}

func (s *Server) handleConn(c net.Conn) {
	sc, chans, reqs, err := ssh.NewServerConn(c, s.config)
	if err != nil {
		s.opts.Logf("sshtest: handshake: %v", err)
		c.Close()
		return
	}
	defer sc.Close()
	if s.opts.SilentGlobalRequests {
		go func() {
			for range reqs { // read and drop: never Reply
			}
		}()
	} else if s.opts.GlobalRequestDelay != nil {
		go func() {
			n := 0
			for req := range reqs {
				n++
				if d := s.opts.GlobalRequestDelay(n); d > 0 {
					time.Sleep(d)
				}
				if req.WantReply {
					// false is what OpenSSH answers an unknown global
					// request with; the point is that it answers at all.
					req.Reply(false, nil)
				}
			}
		}()
	} else {
		go ssh.DiscardRequests(reqs)
	}
	for nc := range chans {
		switch nc.ChannelType() {
		case "session":
			ch, creqs, err := nc.Accept()
			if err != nil {
				continue
			}
			go s.handleSession(ch, creqs)
		case "direct-tcpip":
			go s.handleDirectTCPIP(nc)
		default:
			nc.Reject(ssh.UnknownChannelType, "sshtest: unsupported channel "+nc.ChannelType())
		}
	}
}

type execPayload struct{ Command string }

func (s *Server) handleSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "pty-req", "env", "window-change":
			req.Reply(true, nil)
		case "exec", "shell":
			var cmd string
			if req.Type == "exec" {
				var p execPayload
				if err := ssh.Unmarshal(req.Payload, &p); err != nil {
					req.Reply(false, nil)
					continue
				}
				cmd = p.Command
			}
			req.Reply(true, nil)
			s.mu.Lock()
			s.execs = append(s.execs, cmd)
			s.mu.Unlock()
			status := 127
			if s.opts.Exec != nil {
				status = s.opts.Exec(cmd, ch, ch, ch.Stderr())
			} else {
				io.WriteString(ch.Stderr(), "sshtest: exec not configured\n")
			}
			var buf [4]byte
			binary.BigEndian.PutUint32(buf[:], uint32(status))
			ch.SendRequest("exit-status", false, buf[:])
			return
		default:
			req.Reply(false, nil)
		}
	}
}

type directTCPIPPayload struct {
	Host     string
	Port     uint32
	OrigHost string
	OrigPort uint32
}

func (s *Server) handleDirectTCPIP(nc ssh.NewChannel) {
	var p directTCPIPPayload
	if err := ssh.Unmarshal(nc.ExtraData(), &p); err != nil {
		nc.Reject(ssh.ConnectionFailed, "sshtest: bad direct-tcpip payload")
		return
	}
	requested := net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port)))
	s.mu.Lock()
	s.fwd = append(s.fwd, requested)
	s.mu.Unlock()
	target, ok := s.opts.Resolver[p.Host]
	if !ok {
		nc.Reject(ssh.Prohibited, "sshtest: cannot resolve "+p.Host)
		return
	}
	conn, err := net.Dial("tcp", target)
	if err != nil {
		nc.Reject(ssh.ConnectionFailed, "sshtest: dial "+target+": "+err.Error())
		return
	}
	ch, reqs, err := nc.Accept()
	if err != nil {
		conn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); io.Copy(ch, conn); ch.CloseWrite() }()
	go func() { defer wg.Done(); io.Copy(conn, ch); conn.(*net.TCPConn).CloseWrite() }()
	wg.Wait()
	ch.Close()
	conn.Close()
}
