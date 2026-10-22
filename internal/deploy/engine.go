// Package deploy turns desired container specs into a safe sequence of runtime
// operations. It is runtime-agnostic (it only sees runtime.Runtime) and has two
// phases:
//
//   - Plan reads the host and decides, per app, what must change. It never
//     mutates anything.
//   - Apply executes a plan. A replacement is staged so that the previous
//     version keeps running until the new one has been created, and is restored
//     if any later step fails.
//
// Ownership: barge only manages containers carrying its own labels
// (domain.LabelManagedBy, LabelApp). It never adopts, modifies or deletes a
// container it did not create.
package deploy

import (
	"context"
	"time"

	"github.com/TillmanBuildsTech/barge/internal/runtime"
)

const (
	defaultPollInterval = 100 * time.Millisecond
	defaultStopTimeout  = 10 * time.Second
	// rollbackTimeout bounds cleanup that must finish even after the user
	// cancelled (Ctrl-C): restoring the old version matters more than exiting
	// instantly.
	rollbackTimeout = 2 * time.Minute

	tempNewSuffix = "-barge-new"
	tempOldSuffix = "-barge-old"
)

// Options tune an Engine. Zero values get safe defaults, except Settle where
// zero means "check once, immediately".
type Options struct {
	// Settle is how long a started container must remain running.
	Settle time.Duration
	// PollInterval is how often the container is re-inspected while settling.
	PollInterval time.Duration
	// StopTimeout is the grace period for stopping the old version.
	StopTimeout time.Duration
	// OnEvent, if set, is called synchronously with progress events while
	// applying. It must return quickly and must not call back into the Engine.
	OnEvent func(Event)
}

// Engine plans and applies deployments against one runtime (one host).
type Engine struct {
	rt  runtime.Runtime
	opt Options
}

// New returns an Engine for rt.
func New(rt runtime.Runtime, opt Options) *Engine {
	if opt.PollInterval <= 0 {
		opt.PollInterval = defaultPollInterval
	}
	if opt.StopTimeout <= 0 {
		opt.StopTimeout = defaultStopTimeout
	}
	return &Engine{rt: rt, opt: opt}
}

// detached returns a context that survives cancellation of ctx for cleanup.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
}
