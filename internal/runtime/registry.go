package runtime

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/TillmanBuildsTech/barge/internal/domain"
)

// Registry maps driver names to factories. It is populated explicitly by the
// binary's main (no init() side effects), so what is linked in is visible in
// one place and tests can build their own.
type Registry struct {
	mu sync.RWMutex
	m  map[string]Factory
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{m: map[string]Factory{}} }

// Register adds a driver. Names must be unique and non-empty.
func (r *Registry) Register(name string, f Factory) error {
	if name == "" || f == nil {
		return fmt.Errorf("%w: driver needs a name and a factory", domain.ErrInvalid)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.m[name]; dup {
		return fmt.Errorf("%w: runtime driver %q already registered", domain.ErrConflict, name)
	}
	r.m[name] = f
	return nil
}

// Names lists registered drivers in sorted order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.m))
	for n := range r.m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// New builds the named driver. An unknown name is ErrInvalid and the message
// lists the available drivers.
func (r *Registry) New(name string, d Deps) (Runtime, error) {
	r.mu.RLock()
	f, ok := r.m[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: unknown runtime driver %q (available: %s)",
			domain.ErrInvalid, name, strings.Join(r.Names(), ", "))
	}
	rt, err := f(d)
	if err != nil {
		return nil, fmt.Errorf("init runtime driver %q: %w", name, err)
	}
	return rt, nil
}
