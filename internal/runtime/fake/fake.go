// Package fake is an in-memory runtime.Runtime for tests and dry runs.
package fake

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/TillmanBuildsTech/barge/internal/domain"
	"github.com/TillmanBuildsTech/barge/internal/runtime"
)

// Runtime is an in-memory container runtime.
type Runtime struct {
	mu    sync.Mutex
	seq   int
	byID  map[string]*domain.ContainerState
	names map[string]string // name -> id
}

var _ runtime.Runtime = (*Runtime)(nil)

// New returns an empty fake runtime.
func New() *Runtime {
	return &Runtime{byID: map[string]*domain.ContainerState{}, names: map[string]string{}}
}

// Name implements runtime.Runtime.
func (*Runtime) Name() string { return "fake" }

// Ping implements runtime.Runtime.
func (*Runtime) Ping(context.Context) (runtime.Info, error) {
	return runtime.Info{Driver: "fake", Version: "0"}, nil
}

// Pull implements runtime.Runtime.
func (*Runtime) Pull(context.Context, string) error { return nil }

// Create implements runtime.Runtime.
func (r *Runtime) Create(_ context.Context, spec domain.ContainerSpec) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, taken := r.names[spec.Name]; taken {
		return "", fmt.Errorf("%w: container name %q in use", domain.ErrConflict, spec.Name)
	}
	r.seq++
	id := fmt.Sprintf("fake%04d", r.seq)
	labels := map[string]string{}
	for k, v := range spec.Labels {
		labels[k] = v
	}
	r.byID[id] = &domain.ContainerState{ID: id, Name: spec.Name, Image: spec.Image, Status: domain.StatusCreated, Labels: labels}
	r.names[spec.Name] = id
	return id, nil
}

func (r *Runtime) find(ref string) (*domain.ContainerState, error) {
	if id, ok := r.names[ref]; ok {
		return r.byID[id], nil
	}
	if c, ok := r.byID[ref]; ok {
		return c, nil
	}
	return nil, fmt.Errorf("%w: container %q", domain.ErrNotFound, ref)
}

// Start implements runtime.Runtime.
func (r *Runtime) Start(_ context.Context, ref string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, err := r.find(ref)
	if err != nil {
		return err
	}
	c.Status = domain.StatusRunning
	return nil
}

// Stop implements runtime.Runtime.
func (r *Runtime) Stop(_ context.Context, ref string, _ time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, err := r.find(ref)
	if err != nil {
		return err
	}
	if c.Status == domain.StatusRunning {
		c.Status = domain.StatusExited
	}
	return nil
}

// Remove implements runtime.Runtime.
func (r *Runtime) Remove(_ context.Context, ref string, force bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, err := r.find(ref)
	if err != nil {
		if force {
			return nil
		}
		return err
	}
	if c.Status == domain.StatusRunning && !force {
		return fmt.Errorf("container %q is running: stop it or force removal", ref)
	}
	delete(r.names, c.Name)
	delete(r.byID, c.ID)
	return nil
}

// Rename implements runtime.Runtime.
func (r *Runtime) Rename(_ context.Context, ref, newName string) error {
	if err := domain.ValidateName(newName); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	c, err := r.find(ref)
	if err != nil {
		return err
	}
	if id, taken := r.names[newName]; taken && id != c.ID {
		return fmt.Errorf("%w: container name %q in use", domain.ErrConflict, newName)
	}
	delete(r.names, c.Name)
	c.Name = newName
	r.names[newName] = c.ID
	return nil
}

// Inspect implements runtime.Runtime.
func (r *Runtime) Inspect(_ context.Context, ref string) (domain.ContainerState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, err := r.find(ref)
	if err != nil {
		return domain.ContainerState{}, err
	}
	return clone(*c), nil
}

// List implements runtime.Runtime.
func (r *Runtime) List(_ context.Context, labels map[string]string) ([]domain.ContainerState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.ContainerState
next:
	for _, c := range r.byID {
		for k, v := range labels {
			if c.Labels[k] != v {
				continue next
			}
		}
		out = append(out, clone(*c))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func clone(c domain.ContainerState) domain.ContainerState {
	l := make(map[string]string, len(c.Labels))
	for k, v := range c.Labels {
		l[k] = v
	}
	c.Labels = l
	return c
}
