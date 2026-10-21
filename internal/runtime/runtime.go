// Package runtime defines the container-runtime abstraction barge deploys
// through. Docker, Podman and future runtimes are interchangeable drivers
// selected by name from config; nothing outside driver packages may assume a
// particular runtime.
package runtime

import (
	"context"
	"time"

	"github.com/TillmanBuildsTech/barge/internal/domain"
	"github.com/TillmanBuildsTech/barge/internal/transport"
)

// Info describes a reachable runtime.
type Info struct {
	Driver  string
	Version string
}

// Runtime manages containers on one host. Implementations must return errors
// that wrap domain.ErrNotFound, ErrConflict, ErrInvalid or ErrUnavailable as
// documented below so callers never parse driver-specific text. Every driver
// must pass the suite in package runtimetest.
type Runtime interface {
	Name() string
	// Ping verifies the runtime is reachable (ErrUnavailable otherwise).
	Ping(ctx context.Context) (Info, error)
	// Pull fetches an image.
	Pull(ctx context.Context, ref string) error
	// Create makes a container (not started) and returns its ID. ErrConflict
	// if the name is taken; ErrInvalid if the spec fails validation.
	Create(ctx context.Context, spec domain.ContainerSpec) (string, error)
	// Start, Stop and Remove take a name or ID and return ErrNotFound if it
	// does not exist. Start and Stop are idempotent.
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string, timeout time.Duration) error
	Remove(ctx context.Context, id string, force bool) error
	// Inspect takes a name or ID; ErrNotFound if absent.
	Inspect(ctx context.Context, id string) (domain.ContainerState, error)
	// List returns all containers (running or not) having every given label.
	List(ctx context.Context, labels map[string]string) ([]domain.ContainerState, error)
}

// Deps is what a driver factory receives.
type Deps struct {
	Exec    transport.Executor // how to reach the host
	Options map[string]string  // driver-specific options from config
}

// Factory builds a Runtime from Deps.
type Factory func(Deps) (Runtime, error)
