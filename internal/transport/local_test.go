package transport_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/TillmanBuildsTech/barge/internal/domain"
	"github.com/TillmanBuildsTech/barge/internal/transport"
)

// The test binary re-executes itself as the child process so these tests are
// portable (no reliance on sh, echo, cat...) across linux, macOS and Windows.
func TestMain(m *testing.M) {
	if os.Getenv("BARGE_TEST_HELPER") == "1" {
		helper()
		return
	}
	os.Exit(m.Run())
}

func helper() {
	switch os.Getenv("BARGE_TEST_MODE") {
	case "echo-args":
		fmt.Println(strings.Join(os.Args[1:], "|"))
	case "echo-env":
		fmt.Print(os.Getenv("BARGE_SECRET"))
	case "cat":
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "fail":
		fmt.Fprint(os.Stderr, "boom")
		os.Exit(3)
	case "sleep":
		time.Sleep(30 * time.Second)
	}
	os.Exit(0)
}

func run(ctx context.Context, t *testing.T, mode string, extraArgs []string, cmd transport.Cmd) (transport.Result, error) {
	t.Helper()
	cmd.Argv = append([]string{os.Args[0]}, extraArgs...)
	cmd.Env = append(cmd.Env, "BARGE_TEST_HELPER=1", "BARGE_TEST_MODE="+mode)
	return transport.Local{}.Run(ctx, cmd)
}

func TestLocalPassesArgvVerbatim(t *testing.T) {
	// Metacharacters must reach the child untouched: there is no shell.
	args := []string{"a b", "$HOME", ";rm -rf /", "`x`", "*"}
	res, err := run(t.Context(), t, "echo-args", args, transport.Cmd{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(res.Stdout)), strings.Join(args, "|"); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestLocalEnvAndStdin(t *testing.T) {
	res, err := run(t.Context(), t, "echo-env", nil, transport.Cmd{Env: []string{"BARGE_SECRET=s3cret"}})
	if err != nil || string(res.Stdout) != "s3cret" {
		t.Fatalf("env: res=%q err=%v", res.Stdout, err)
	}
	res, err = run(t.Context(), t, "cat", nil, transport.Cmd{Stdin: strings.NewReader("hello")})
	if err != nil || string(res.Stdout) != "hello" {
		t.Fatalf("stdin: res=%q err=%v", res.Stdout, err)
	}
}

func TestLocalNonZeroExit(t *testing.T) {
	res, err := run(t.Context(), t, "fail", nil, transport.Cmd{})
	var ee *transport.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("want *ExitError, got %v", err)
	}
	if ee.Code != 3 || res.ExitCode != 3 || string(res.Stderr) != "boom" {
		t.Errorf("code=%d res=%+v", ee.Code, res)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should include stderr: %v", err)
	}
}

func TestLocalContextCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := run(ctx, t, "sleep", nil, transport.Cmd{})
	if err == nil {
		t.Fatal("want error on cancel")
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("cancel was not honoured, took %v", time.Since(start))
	}
}

func TestLocalMissingBinaryIsUnavailable(t *testing.T) {
	_, err := transport.Local{}.Run(t.Context(), transport.Cmd{Argv: []string{"barge-no-such-binary-xyz"}})
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestLocalEmptyArgv(t *testing.T) {
	_, err := transport.Local{}.Run(t.Context(), transport.Cmd{})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
}
