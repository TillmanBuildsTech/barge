package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"time"

	"github.com/TillmanBuildsTech/barge/internal/domain"
)

// Local runs commands on the machine barge itself runs on. It works on Linux,
// macOS and Windows because it uses os/exec directly and no shell.
type Local struct{}

var _ Executor = Local{}

// Run implements Executor.
func (Local) Run(ctx context.Context, c Cmd) (Result, error) {
	if len(c.Argv) == 0 || c.Argv[0] == "" {
		return Result{}, fmt.Errorf("%w: empty command", domain.ErrInvalid)
	}
	// #nosec G204 -- executing the configured container CLI with a
	// caller-built argv is this package's purpose; no shell is involved.
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.Env = append(os.Environ(), c.Env...)
	cmd.Stdin = c.Stdin
	cmd.WaitDelay = 5 * time.Second

	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil {
		return res, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return res, fmt.Errorf("run %s: %w", c.Argv[0], ctxErr)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		return res, &ExitError{Code: res.ExitCode, Stderr: stderr.String()}
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return res, fmt.Errorf("%w: %s not found in PATH: %w", domain.ErrUnavailable, c.Argv[0], err)
	}
	return res, fmt.Errorf("run %s: %w", c.Argv[0], err)
}
