// Package sshtest is an in-process SSH server for tests. Commands it receives
// run through a real `sh -c` (as sshd does), so quoting bugs in the client are
// caught end to end. It requires a POSIX sh; tests should skip on Windows.
package sshtest

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// NewClientKey writes a fresh ed25519 private key (mode 0600) to a temp dir.
func NewClientKey(t testing.TB) (path string, pub ssh.PublicKey) {
	t.Helper()
	p, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blk, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(blk), 0o600); err != nil {
		t.Fatal(err)
	}
	pub, err = ssh.NewPublicKey(p)
	if err != nil {
		t.Fatal(err)
	}
	return path, pub
}

// WriteKnownHosts writes a known_hosts file trusting key for host:port.
func WriteKnownHosts(t testing.TB, host string, port int, key ssh.PublicKey) string {
	t.Helper()
	line := knownhosts.Line([]string{net.JoinHostPort(host, strconv.Itoa(port))}, key)
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Server is a test SSH server.
type Server struct {
	ln      net.Listener
	hostKey ssh.PublicKey
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	conns atomic.Int32

	mu       sync.Mutex
	commands []string
	live     map[net.Conn]struct{}
}

// Option customises a Server.
type Option func(*ssh.ServerConfig, testing.TB)

// WithECDSAHostKey makes the server also offer an ECDSA host key. Clients
// prefer ECDSA over ed25519, so this reproduces "server offers a key type that
// is not in known_hosts".
func WithECDSAHostKey() Option {
	return func(cfg *ssh.ServerConfig, t testing.TB) {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		sg, err := ssh.NewSignerFromKey(k)
		if err != nil {
			t.Fatal(err)
		}
		cfg.AddHostKey(sg)
	}
}

// New starts a server that accepts only `user` authenticating with `authorized`.
func New(t testing.TB, user string, authorized ssh.PublicKey, opts ...Option) *Server {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if c.User() == user && string(k.Marshal()) == string(authorized.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("unauthorized")
		},
	}
	cfg.AddHostKey(signer)
	for _, o := range opts {
		o(cfg, t)
	}

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{ln: ln, hostKey: signer.PublicKey(), live: map[net.Conn]struct{}{}}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.live[nc] = struct{}{}
			s.mu.Unlock()
			s.wg.Add(1)
			go s.handle(nc, cfg)
		}
	}()
	t.Cleanup(func() {
		s.cancel()
		_ = ln.Close()
		s.DropConns()
		s.wg.Wait()
	})
	return s
}

// Host returns the listen IP.
func (s *Server) Host() string { return "127.0.0.1" }

// Port returns the listen port.
func (s *Server) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

// HostKey returns the server's public host key.
func (s *Server) HostKey() ssh.PublicKey { return s.hostKey }

// Conns is the number of successfully authenticated connections so far.
func (s *Server) Conns() int { return int(s.conns.Load()) }

// Commands returns every exec command string received, verbatim.
func (s *Server) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

// DropConns abruptly closes all live connections.
func (s *Server) DropConns() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.live {
		_ = c.Close()
	}
}

func (s *Server) handle(nc net.Conn, cfg *ssh.ServerConfig) {
	defer s.wg.Done()
	defer func() { _ = nc.Close() }()
	conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	s.conns.Add(1)
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		if nch.ChannelType() != "session" {
			_ = nch.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		ch, creqs, err := nch.Accept()
		if err != nil {
			continue
		}
		s.wg.Add(1)
		go s.session(ch, creqs)
	}
	_ = conn.Close()
}

func (s *Server) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer s.wg.Done()
	defer func() { _ = ch.Close() }()
	for req := range reqs {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			continue
		}
		var p struct{ Command string }
		if err := ssh.Unmarshal(req.Payload, &p); err != nil {
			_ = req.Reply(false, nil)
			return
		}
		_ = req.Reply(true, nil)
		s.mu.Lock()
		s.commands = append(s.commands, p.Command)
		s.mu.Unlock()

		// The test server deliberately executes what the client sent through a
		// real shell, exactly as sshd does; that is what the tests verify.
		cmd := exec.CommandContext(s.ctx, "sh", "-c", p.Command) //nolint:gosec // intentional
		cmd.Stdin, cmd.Stdout, cmd.Stderr = ch, ch, ch.Stderr()
		cmd.WaitDelay = time.Second
		code := 0
		if err := cmd.Run(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			} else {
				code = 127
			}
		}
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)})) //nolint:gosec // small non-negative
		return
	}
}
