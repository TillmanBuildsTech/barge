// Package transport abstracts "run a command somewhere": on this machine or on
// a remote host. Runtime drivers speak to the container CLI exclusively through
// an Executor, which keeps them independent of how the host is reached.
package transport

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// Cmd is a command to execute. Argv is passed as discrete arguments and is
// never interpreted by a shell by the local transport.
type Cmd struct {
	Argv  []string  // Argv[0] is the program
	Env   []string  // extra "KEY=VALUE" entries, in addition to the inherited environment
	Stdin io.Reader // optional
}

// Result is the captured output of a finished command.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Executor runs commands. Implementations must honour ctx cancellation and
// always return the captured Result, even alongside an error.
//
// A command that runs but exits non-zero yields a *ExitError. Failure to start
// or reach the host yields another error (wrapping domain.ErrUnavailable
// where appropriate).
type Executor interface {
	Run(ctx context.Context, cmd Cmd) (Result, error)
}

// ExitError reports a command that ran and exited non-zero.
type ExitError struct {
	Code   int
	Stderr string
}

// Error implements error.
func (e *ExitError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		return fmt.Sprintf("exit status %d", e.Code)
	}
	return fmt.Sprintf("exit status %d: %s", e.Code, msg)
}
