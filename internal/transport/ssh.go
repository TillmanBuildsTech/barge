package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/TillmanBuildsTech/barge/internal/domain"
)

// ErrHostKey means the remote host's key could not be verified against
// known_hosts (unknown host, or a key that differs from the recorded one).
// It is deliberately NOT domain.ErrUnavailable: a mismatch may be an attack,
// so callers must not treat it as a transient failure to retry.
var ErrHostKey = errors.New("host key verification failed")

// DefaultConnectTimeout applies when SSHConfig.ConnectTimeout is zero.
const DefaultConnectTimeout = 30 * time.Second

// SSHConfig describes how to reach a remote Linux host. There is no option to
// skip host-key verification.
type SSHConfig struct {
	Host           string
	Port           int
	User           string
	IdentityFile   string // required; "~/" is expanded
	KnownHosts     string // default ~/.ssh/known_hosts; must exist
	ConnectTimeout time.Duration
}

// SSH is an Executor that runs commands on a remote host over one lazily
// opened, reused SSH connection. It is safe for concurrent use.
type SSH struct {
	cfg    SSHConfig
	addr   string
	signer ssh.Signer
	check  ssh.HostKeyCallback

	mu     sync.Mutex
	client *ssh.Client
}

var _ Executor = (*SSH)(nil)

// NewSSH validates the configuration and loads the key and known_hosts file
// now, without touching the network, so misconfiguration fails at startup.
func NewSSH(cfg SSHConfig) (*SSH, error) {
	switch {
	case cfg.Host == "":
		return nil, fmt.Errorf("%w: ssh host is required", domain.ErrInvalid)
	case cfg.User == "":
		return nil, fmt.Errorf("%w: ssh user is required", domain.ErrInvalid)
	case cfg.Port < 1 || cfg.Port > 65535:
		return nil, fmt.Errorf("%w: ssh port %d must be 1-65535", domain.ErrInvalid, cfg.Port)
	case cfg.IdentityFile == "":
		return nil, fmt.Errorf("%w: ssh identity_file is required", domain.ErrInvalid)
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = DefaultConnectTimeout
	}

	signer, err := loadSigner(cfg.IdentityFile)
	if err != nil {
		return nil, err
	}

	khPath := cfg.KnownHosts
	if khPath == "" {
		khPath = "~/.ssh/known_hosts"
	}
	khPath, err = expandHome(khPath)
	if err != nil {
		return nil, err
	}
	check, err := knownhosts.New(khPath)
	if err != nil {
		return nil, fmt.Errorf("load known_hosts (host keys are always verified): %w", err)
	}

	return &SSH{
		cfg: cfg, signer: signer, check: check,
		addr: net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
	}, nil
}

func expandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand %q: %w", p, err)
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
}

func loadSigner(path string) (ssh.Signer, error) {
	path, err := expandHome(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("identity file: %w", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: identity file %s has permissions %04o; it must not be accessible to group or others (chmod 600)",
			domain.ErrInvalid, path, info.Mode().Perm())
	}
	pem, err := os.ReadFile(path) //nolint:gosec // user-configured key path
	if err != nil {
		return nil, fmt.Errorf("identity file: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			return nil, fmt.Errorf("%w: identity file %s is passphrase-protected, which is not supported yet", domain.ErrInvalid, path)
		}
		return nil, fmt.Errorf("%w: parse identity file %s: %w", domain.ErrInvalid, path, err)
	}
	return signer, nil
}

// Close drops the connection. The SSH can be reused afterwards (it reconnects).
func (s *SSH) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropLocked()
}

func (s *SSH) dropLocked() error {
	if s.client == nil {
		return nil
	}
	err := s.client.Close()
	s.client = nil
	return err
}

// hostKeyCallback verifies against known_hosts. If the server presented a key
// of a type known_hosts has no entry for (while it does have other keys for
// the host), that is an algorithm-negotiation miss, not an attack: the types
// the host is known by are reported through *want so connect can retry
// restricted to them. A same-type key that differs is always a hard failure.
func (s *SSH) hostKeyCallback(want *[]string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := s.check(hostname, remote, key)
		if err == nil {
			return nil
		}
		var ke *knownhosts.KeyError
		switch {
		case errors.As(err, &ke) && len(ke.Want) == 0:
			return fmt.Errorf("%w: %s is not in known_hosts (verify its fingerprint %s, then add it, e.g. with ssh-keyscan)",
				ErrHostKey, hostname, ssh.FingerprintSHA256(key))
		case errors.As(err, &ke):
			sameType := false
			var algs []string
			for _, w := range ke.Want {
				if w.Key.Type() == key.Type() {
					sameType = true
				}
				algs = append(algs, algorithmsFor(w.Key.Type())...)
			}
			if !sameType {
				*want = algs
			}
			return fmt.Errorf("%w: the key presented by %s (%s) does not match known_hosts; this may be a man-in-the-middle attack",
				ErrHostKey, hostname, ssh.FingerprintSHA256(key))
		default:
			return fmt.Errorf("%w: %w", ErrHostKey, err)
		}
	}
}

// algorithmsFor maps a host key type to the signature algorithms that use it.
func algorithmsFor(keyType string) []string {
	if keyType == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	}
	return []string{keyType}
}

func (s *SSH) connect(ctx context.Context) (*ssh.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		return s.client, nil
	}
	client, want, err := s.dial(ctx, nil)
	if err != nil && errors.Is(err, ErrHostKey) && len(want) > 0 {
		client, _, err = s.dial(ctx, want)
	}
	if err != nil {
		return nil, err
	}
	s.client = client
	return client, nil
}

func (s *SSH) dial(ctx context.Context, algs []string) (*ssh.Client, []string, error) {
	d := net.Dialer{Timeout: s.cfg.ConnectTimeout}
	nc, err := d.DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: dial %s: %w", domain.ErrUnavailable, s.addr, err)
	}
	_ = nc.SetDeadline(time.Now().Add(s.cfg.ConnectTimeout)) // bound the handshake
	var want []string
	cc := &ssh.ClientConfig{
		User:              s.cfg.User,
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(s.signer)},
		HostKeyCallback:   s.hostKeyCallback(&want),
		HostKeyAlgorithms: algs,
		Timeout:           s.cfg.ConnectTimeout,
	}
	conn, chans, reqs, err := ssh.NewClientConn(nc, s.addr, cc)
	if err != nil {
		_ = nc.Close()
		if errors.Is(err, ErrHostKey) {
			return nil, want, fmt.Errorf("connect %s: %w", s.addr, err)
		}
		return nil, nil, fmt.Errorf("%w: ssh handshake with %s: %w", domain.ErrUnavailable, s.addr, err)
	}
	_ = nc.SetDeadline(time.Time{})
	return ssh.NewClient(conn, chans, reqs), nil, nil
}

// Run implements Executor.
func (s *SSH) Run(ctx context.Context, c Cmd) (Result, error) {
	command, preamble, err := buildRemote(c)
	if err != nil {
		return Result{}, err
	}
	client, err := s.connect(ctx)
	if err != nil {
		return Result{}, err
	}
	sess, err := client.NewSession()
	if err != nil {
		// The cached connection is dead; forget it so the next call reconnects.
		s.mu.Lock()
		_ = s.dropLocked()
		s.mu.Unlock()
		return Result{}, fmt.Errorf("%w: open ssh session on %s: %w", domain.ErrUnavailable, s.addr, err)
	}
	defer func() { _ = sess.Close() }()

	var stdout, stderr bytes.Buffer
	sess.Stdout, sess.Stderr = &stdout, &stderr
	switch {
	case preamble != "" && c.Stdin != nil:
		sess.Stdin = io.MultiReader(strings.NewReader(preamble), c.Stdin)
	case preamble != "":
		sess.Stdin = strings.NewReader(preamble)
	default:
		sess.Stdin = c.Stdin
	}

	done := make(chan error, 1)
	go func() { done <- sess.Run(command) }()

	select {
	case err = <-done:
	case <-ctx.Done():
		_ = sess.Close()
		<-done // let the goroutine finish before the buffers are read
		return Result{}, fmt.Errorf("run %s on %s: %w", c.Argv[0], s.addr, ctx.Err())
	}

	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil {
		return res, nil
	}
	var ee *ssh.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitStatus()
		return res, &ExitError{Code: res.ExitCode, Stderr: stderr.String()}
	}
	return res, fmt.Errorf("run %s on %s: %w", c.Argv[0], s.addr, err)
}
