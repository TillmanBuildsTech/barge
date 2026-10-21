package transport_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/TillmanBuildsTech/barge/internal/domain"
	"github.com/TillmanBuildsTech/barge/internal/transport"
	"github.com/TillmanBuildsTech/barge/internal/transport/sshtest"
)

func needSh(t testing.TB) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test sshd runs commands through a POSIX sh")
	}
}

type fixture struct {
	srv *sshtest.Server
	cfg transport.SSHConfig
	key ssh.PublicKey
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	keyPath, pub := sshtest.NewClientKey(t)
	srv := sshtest.New(t, "deploy", pub)
	return fixture{srv: srv, key: pub, cfg: transport.SSHConfig{
		Host: srv.Host(), Port: srv.Port(), User: "deploy",
		IdentityFile:   keyPath,
		KnownHosts:     sshtest.WriteKnownHosts(t, srv.Host(), srv.Port(), srv.HostKey()),
		ConnectTimeout: 5 * time.Second,
	}}
}

func (f fixture) client(t *testing.T) *transport.SSH {
	t.Helper()
	c, err := transport.NewSSH(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// ---- quoting -------------------------------------------------------------

func TestShellQuoteGolden(t *testing.T) {
	for in, want := range map[string]string{
		"":         `''`,
		"abc":      `abc`,
		"docker":   `docker`,
		"a b":      `'a b'`,
		"it's":     `'it'\''s'`,
		"$HOME":    `'$HOME'`,
		"a;b":      `'a;b'`,
		"x=1":      `x=1`,
		"line\nbr": "'line\nbr'",
	} {
		if got := transport.ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestShellQuoteRoundTripsThroughSh(t *testing.T) {
	needSh(t)
	for _, in := range []string{"", "plain", "a b", "it's", `"dq"`, "$(id)", "`id`", ";&|<>", "*?[", "~", "line\nbreak", "tab\there", "ünï😀", `back\slash`, "-n", "--"} {
		out, err := exec.Command("sh", "-c", "printf %s "+transport.ShellQuote(in)).Output()
		if err != nil || string(out) != in {
			t.Errorf("%q round-tripped to %q (err=%v)", in, out, err)
		}
	}
}

func FuzzShellQuote(f *testing.F) {
	f.Add("it's")
	f.Add("$(x)")
	f.Fuzz(func(t *testing.T, in string) {
		needSh(t)
		if strings.ContainsRune(in, 0) {
			t.Skip()
		}
		out, err := exec.Command("sh", "-c", "printf %s "+transport.ShellQuote(in)).Output()
		if err != nil || string(out) != in {
			t.Fatalf("%q -> %q (err=%v)", in, out, err)
		}
	})
}

func TestBuildRemoteValidation(t *testing.T) {
	tests := []struct {
		name string
		cmd  transport.Cmd
	}{
		{"empty argv", transport.Cmd{}},
		{"NUL in arg", transport.Cmd{Argv: []string{"a\x00b"}}},
		{"env without equals", transport.Cmd{Argv: []string{"x"}, Env: []string{"NOEQUALS"}}},
		{"env bad key", transport.Cmd{Argv: []string{"x"}, Env: []string{"A B=1"}}},
		{"env key starts with digit", transport.Cmd{Argv: []string{"x"}, Env: []string{"1A=1"}}},
		{"env key injection", transport.Cmd{Argv: []string{"x"}, Env: []string{"A;rm -rf /=1"}}},
		{"NUL in env value", transport.Cmd{Argv: []string{"x"}, Env: []string{"A=\x00"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := transport.BuildRemote(tt.cmd); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}
}

func TestBuildRemoteKeepsEnvValuesOutOfCommand(t *testing.T) {
	cmd, preamble, err := transport.BuildRemote(transport.Cmd{Argv: []string{"docker", "ps"}, Env: []string{"TOKEN=hunter2"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cmd, "hunter2") {
		t.Fatalf("secret in command string: %s", cmd)
	}
	if !strings.Contains(preamble, "hunter2") {
		t.Fatalf("secret should be in stdin preamble: %q", preamble)
	}
	if _, p, _ := transport.BuildRemote(transport.Cmd{Argv: []string{"docker"}}); p != "" {
		t.Errorf("no env must mean no preamble, got %q", p)
	}
}

// ---- construction (no network) ------------------------------------------

func TestNewSSHFailsFast(t *testing.T) {
	f := newFixture(t)
	tmp := t.TempDir()
	mut := func(fn func(*transport.SSHConfig)) transport.SSHConfig { c := f.cfg; fn(&c); return c }

	tests := []struct {
		name string
		cfg  transport.SSHConfig
		want error
	}{
		{"no host", mut(func(c *transport.SSHConfig) { c.Host = "" }), domain.ErrInvalid},
		{"no user", mut(func(c *transport.SSHConfig) { c.User = "" }), domain.ErrInvalid},
		{"bad port", mut(func(c *transport.SSHConfig) { c.Port = 0 }), domain.ErrInvalid},
		{"no identity file", mut(func(c *transport.SSHConfig) { c.IdentityFile = "" }), domain.ErrInvalid},
		{"identity file missing", mut(func(c *transport.SSHConfig) { c.IdentityFile = filepath.Join(tmp, "nope") }), os.ErrNotExist},
		{"known_hosts missing fails closed", mut(func(c *transport.SSHConfig) { c.KnownHosts = filepath.Join(tmp, "nope") }), os.ErrNotExist},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := transport.NewSSH(tt.cfg); !errors.Is(err, tt.want) {
				t.Fatalf("want %v, got %v", tt.want, err)
			}
		})
	}

	t.Run("garbage key", func(t *testing.T) {
		p := filepath.Join(tmp, "junk")
		_ = os.WriteFile(p, []byte("not a key"), 0o600)
		if _, err := transport.NewSSH(mut(func(c *transport.SSHConfig) { c.IdentityFile = p })); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("group-readable key rejected", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX permissions")
		}
		if err := os.Chmod(f.cfg.IdentityFile, 0o640); err != nil {
			t.Fatal(err)
		}
		if _, err := transport.NewSSH(f.cfg); !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "permissions") {
			t.Fatalf("want ErrInvalid about permissions, got %v", err)
		}
	})
}

// ---- end to end ----------------------------------------------------------

func TestSSHPassesArgvVerbatim(t *testing.T) {
	needSh(t)
	c := newFixture(t).client(t)
	args := []string{"a b", "$HOME", ";rm -rf /", "`id`", "*", "it's", "$(id)", "line\nbreak", ""}
	res, err := c.Run(t.Context(), transport.Cmd{Argv: append([]string{"printf", "%s|"}, args...)})
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Join(args, "|") + "|"; string(res.Stdout) != want {
		t.Errorf("stdout = %q, want %q", res.Stdout, want)
	}
}

func TestSSHEnvTravelsOnStdinNotInCommand(t *testing.T) {
	needSh(t)
	f := newFixture(t)
	c := f.client(t)

	res, err := c.Run(t.Context(), transport.Cmd{
		Argv: []string{"sh", "-c", `printf '%s/%s' "$BARGE_A" "$BARGE_B"`},
		Env:  []string{"BARGE_A=hunter2", "BARGE_B=it's $(id) \"q\"\nnl"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "hunter2/it's $(id) \"q\"\nnl"; string(res.Stdout) != want {
		t.Errorf("stdout = %q, want %q", res.Stdout, want)
	}
	for _, cmd := range f.srv.Commands() {
		if strings.Contains(cmd, "hunter2") {
			t.Errorf("secret visible in the command line sshd (and ps) sees: %s", cmd)
		}
	}
}

func TestSSHEnvAndStdinTogether(t *testing.T) {
	needSh(t)
	c := newFixture(t).client(t)
	res, err := c.Run(t.Context(), transport.Cmd{
		Argv:  []string{"cat"},
		Env:   []string{"X=1"},
		Stdin: strings.NewReader("payload"),
	})
	if err != nil || string(res.Stdout) != "payload" {
		t.Fatalf("res=%q err=%v", res.Stdout, err)
	}
}

func TestSSHNonZeroExit(t *testing.T) {
	needSh(t)
	c := newFixture(t).client(t)
	res, err := c.Run(t.Context(), transport.Cmd{Argv: []string{"sh", "-c", "echo boom >&2; exit 3"}})
	var ee *transport.ExitError
	if !errors.As(err, &ee) || ee.Code != 3 || res.ExitCode != 3 || strings.TrimSpace(string(res.Stderr)) != "boom" {
		t.Fatalf("err=%v res=%+v", err, res)
	}
}

func TestSSHReusesOneConnection(t *testing.T) {
	needSh(t)
	f := newFixture(t)
	c := f.client(t)
	for range 3 {
		if _, err := c.Run(t.Context(), transport.Cmd{Argv: []string{"true"}}); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.srv.Conns(); n != 1 {
		t.Errorf("connections = %d, want 1", n)
	}
}

func TestSSHRecoversAfterConnectionDrop(t *testing.T) {
	needSh(t)
	f := newFixture(t)
	c := f.client(t)
	if _, err := c.Run(t.Context(), transport.Cmd{Argv: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	f.srv.DropConns()
	if _, err := c.Run(t.Context(), transport.Cmd{Argv: []string{"true"}}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("after drop: want ErrUnavailable, got %v", err)
	}
	if _, err := c.Run(t.Context(), transport.Cmd{Argv: []string{"true"}}); err != nil {
		t.Fatalf("should reconnect on next call: %v", err)
	}
	if n := f.srv.Conns(); n != 2 {
		t.Errorf("connections = %d, want 2", n)
	}
}

func TestSSHContextCancel(t *testing.T) {
	needSh(t)
	c := newFixture(t).client(t)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Run(ctx, transport.Cmd{Argv: []string{"sleep", "30"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("cancel not honoured: %v", time.Since(start))
	}
}

func TestSSHInvalidCmdDoesNotConnect(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	if _, err := c.Run(t.Context(), transport.Cmd{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if f.srv.Conns() != 0 {
		t.Error("an invalid command must not open a connection")
	}
}

// ---- security ------------------------------------------------------------

func TestSSHRejectsChangedHostKeyAndRunsNothing(t *testing.T) {
	needSh(t)
	f := newFixture(t)
	_, otherPub := sshtest.NewClientKey(t) // any key that is not the server's
	f.cfg.KnownHosts = sshtest.WriteKnownHosts(t, f.srv.Host(), f.srv.Port(), otherPub)
	c := f.client(t)

	_, err := c.Run(t.Context(), transport.Cmd{Argv: []string{"touch", "/tmp/should-never-exist"}})
	if !errors.Is(err, transport.ErrHostKey) {
		t.Fatalf("want ErrHostKey, got %v", err)
	}
	if errors.Is(err, domain.ErrUnavailable) {
		t.Error("a key mismatch must not look like a transient outage")
	}
	if cmds := f.srv.Commands(); len(cmds) != 0 {
		t.Errorf("nothing may execute on an unverified host: %v", cmds)
	}
}

func TestSSHHandlesServerOfferingKeyTypeAbsentFromKnownHosts(t *testing.T) {
	needSh(t)
	keyPath, pub := sshtest.NewClientKey(t)
	// Server offers ECDSA (preferred by clients) and ed25519; known_hosts only
	// has the ed25519 key. This is a legitimate host and must connect.
	srv := sshtest.New(t, "deploy", pub, sshtest.WithECDSAHostKey())
	cfg := transport.SSHConfig{
		Host: srv.Host(), Port: srv.Port(), User: "deploy", IdentityFile: keyPath,
		KnownHosts: sshtest.WriteKnownHosts(t, srv.Host(), srv.Port(), srv.HostKey()),
	}
	c, err := transport.NewSSH(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := c.Run(t.Context(), transport.Cmd{Argv: []string{"true"}}); err != nil {
		t.Fatalf("legitimate host rejected: %v", err)
	}
}

func TestSSHStillRejectsWrongKeyOfSameType(t *testing.T) {
	f := newFixture(t)
	// known_hosts has a different ed25519 key than the server's.
	_, other := sshtest.NewClientKey(t)
	f.cfg.KnownHosts = sshtest.WriteKnownHosts(t, f.srv.Host(), f.srv.Port(), other)
	_, err := f.client(t).Run(t.Context(), transport.Cmd{Argv: []string{"true"}})
	if !errors.Is(err, transport.ErrHostKey) || !strings.Contains(err.Error(), "man-in-the-middle") {
		t.Fatalf("want ErrHostKey/MITM warning, got %v", err)
	}
}

func TestSSHRejectsUnknownHost(t *testing.T) {
	f := newFixture(t)
	empty := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f.cfg.KnownHosts = empty
	_, err := f.client(t).Run(t.Context(), transport.Cmd{Argv: []string{"true"}})
	if !errors.Is(err, transport.ErrHostKey) || !strings.Contains(err.Error(), "known_hosts") {
		t.Fatalf("want ErrHostKey mentioning known_hosts, got %v", err)
	}
}

func TestSSHAuthFailure(t *testing.T) {
	f := newFixture(t)
	f.cfg.User = "intruder"
	_, err := f.client(t).Run(t.Context(), transport.Cmd{Argv: []string{"true"}})
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestSSHUnreachableHost(t *testing.T) {
	f := newFixture(t)
	f.cfg.Port = 1 // nothing listens here
	f.cfg.ConnectTimeout = time.Second
	_, err := f.client(t).Run(t.Context(), transport.Cmd{Argv: []string{"true"}})
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}
