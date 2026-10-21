// Package transporttest provides a scripted transport.Executor for tests.
package transporttest

import (
	"context"
	"sync"

	"github.com/TillmanBuildsTech/barge/internal/transport"
)

// Response is one scripted reply. A non-zero ExitCode yields a
// *transport.ExitError; Err, if set, is returned as-is (a start failure).
type Response struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error
}

// Fake records every call and replays Responses in order. Once the script is
// exhausted it returns empty successes.
type Fake struct {
	mu        sync.Mutex
	Responses []Response
	Calls     []transport.Cmd
}

var _ transport.Executor = (*Fake)(nil)

// Run implements transport.Executor.
func (f *Fake) Run(_ context.Context, c transport.Cmd) (transport.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, c)
	if len(f.Responses) == 0 {
		return transport.Result{}, nil
	}
	r := f.Responses[0]
	f.Responses = f.Responses[1:]
	res := transport.Result{Stdout: []byte(r.Stdout), Stderr: []byte(r.Stderr), ExitCode: r.ExitCode}
	switch {
	case r.Err != nil:
		return res, r.Err
	case r.ExitCode != 0:
		return res, &transport.ExitError{Code: r.ExitCode, Stderr: r.Stderr}
	}
	return res, nil
}
