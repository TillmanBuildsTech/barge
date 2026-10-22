// Package session is the one place that knows how to turn a config into
// connected, planned and applied targets. The CLI commands and the TUI are both
// thin clients of it, so they cannot drift apart in safety behaviour:
// secrets are resolved first, every target is planned before anything is
// changed, and a problem anywhere means nothing is applied anywhere.
package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/TillmanBuildsTech/barge/internal/config"
	"github.com/TillmanBuildsTech/barge/internal/deploy"
	"github.com/TillmanBuildsTech/barge/internal/domain"
	"github.com/TillmanBuildsTech/barge/internal/runtime"
	"github.com/TillmanBuildsTech/barge/internal/secrets"
	"github.com/TillmanBuildsTech/barge/internal/transport"
)

// Event is a deploy.Event tagged with the target it happened on.
type Event struct {
	Target string
	deploy.Event
}

// TargetRun is everything known about one target.
type TargetRun struct {
	Target  config.Target
	Plan    deploy.Plan
	Err     error // could not resolve secrets, connect, or plan
	Results []deploy.Result
	Applied bool

	engine *deploy.Engine
	closer io.Closer
}

// PlanOK reports whether the target planned cleanly with nothing blocked.
func (tr *TargetRun) PlanOK() bool { return tr.Err == nil && !tr.Plan.HasBlocked() }

// OK reports whether the target is healthy: it planned cleanly and, if it was
// applied, every app succeeded.
func (tr *TargetRun) OK() bool {
	return tr.PlanOK() && (!tr.Applied || !deploy.AnyFailed(tr.Results))
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// Session plans and applies a set of targets. It is not safe for concurrent
// use, except that the observer may be invoked from the applying goroutine.
type Session struct {
	cfg     *config.Config
	reg     *runtime.Registry
	targets []config.Target

	runs      []*TargetRun
	allValues []string // every secret value ever resolved, for redaction

	mu       sync.Mutex
	observer func(Event)
}

// New creates a session for the given targets.
func New(cfg *config.Config, reg *runtime.Registry, targets []config.Target) *Session {
	return &Session{cfg: cfg, reg: reg, targets: targets}
}

// SelectTargets returns the named target, or all of them when name is empty.
func SelectTargets(cfg *config.Config, name string) ([]config.Target, error) {
	if name == "" {
		return cfg.Targets, nil
	}
	var known []string
	for _, t := range cfg.Targets {
		if t.Name == name {
			return []config.Target{t}, nil
		}
		known = append(known, t.Name)
	}
	return nil, fmt.Errorf("unknown target %q (available: %s)", name, strings.Join(known, ", "))
}

// Runs returns the current per-target state.
func (s *Session) Runs() []*TargetRun { return s.runs }

// Redactor masks every secret value this session has resolved.
func (s *Session) Redactor() *secrets.Redactor { return secrets.NewRedactor(s.allValues) }

// PlanOK reports whether every target planned cleanly with nothing blocked.
func (s *Session) PlanOK() bool {
	for _, r := range s.runs {
		if !r.PlanOK() {
			return false
		}
	}
	return true
}

// OK reports whether every target is healthy (see TargetRun.OK).
func (s *Session) OK() bool {
	for _, r := range s.runs {
		if !r.OK() {
			return false
		}
	}
	return true
}

// Changes counts the apps that apply would change.
func (s *Session) Changes() int {
	n := 0
	for _, r := range s.runs {
		if r.Err != nil {
			continue
		}
		for _, it := range r.Plan.Items {
			if it.Action != deploy.ActionNoop && it.Action != deploy.ActionBlocked {
				n++
			}
		}
	}
	return n
}

// Plan (re)connects to every target and plans it, replacing any earlier plan.
// Secrets are resolved afresh each time, so a rotated secret shows up as a
// change on the next Plan. Nothing on any host is modified.
func (s *Session) Plan(ctx context.Context) []*TargetRun {
	_ = s.closeRuns()
	cache := &secretCache{byApp: map[string]resolvedApp{}}
	s.runs = make([]*TargetRun, 0, len(s.targets))
	for _, t := range s.targets {
		s.runs = append(s.runs, s.planTarget(ctx, t, cache))
	}
	s.allValues = append(s.allValues, cache.values...)
	return s.runs
}

// Apply executes the plan on every target. It refuses (changing nothing) unless
// Plan has run and every target planned cleanly. observe, if non-nil, receives
// progress events as they happen.
func (s *Session) Apply(ctx context.Context, observe func(Event)) error {
	if len(s.runs) == 0 {
		return errors.New("nothing to apply: run Plan first")
	}
	for _, r := range s.runs {
		if r.Applied {
			return errors.New("already applied: run Plan again before applying again")
		}
	}
	if !s.PlanOK() {
		return fmt.Errorf("%w: refusing to apply, nothing was changed: the plan has problems", domain.ErrConflict)
	}
	s.setObserver(observe)
	defer s.setObserver(nil)
	for _, r := range s.runs {
		results, err := r.engine.Apply(ctx, r.Plan)
		if err != nil { // only possible for a blocked plan, excluded above
			r.Err = err
		}
		r.Results, r.Applied = results, true
	}
	return nil
}

// Close releases every connection. It is safe to call repeatedly.
func (s *Session) Close() error { return s.closeRuns() }

func (s *Session) closeRuns() error {
	var errs []error
	for _, r := range s.runs {
		if r.closer != nil {
			errs = append(errs, r.closer.Close())
			r.closer = nil
		}
	}
	s.runs = nil
	return errors.Join(errs...)
}

func (s *Session) setObserver(f func(Event)) {
	s.mu.Lock()
	s.observer = f
	s.mu.Unlock()
}

func (s *Session) emit(target string, ev deploy.Event) {
	s.mu.Lock()
	f := s.observer
	s.mu.Unlock()
	if f != nil {
		f(Event{Target: target, Event: ev})
	}
}

// executorFor builds the transport for a target: this machine, or SSH to a
// remote Linux host with verified host keys.
func executorFor(t config.Target) (transport.Executor, io.Closer, error) {
	switch t.Type {
	case config.TargetLocal:
		return transport.Local{}, nopCloser{}, nil
	case config.TargetSSH:
		c, err := transport.NewSSH(transport.SSHConfig{
			Host: t.SSH.Host, Port: t.SSH.Port, User: t.SSH.User,
			IdentityFile: t.SSH.IdentityFile, KnownHosts: t.SSH.KnownHosts,
			ConnectTimeout: t.SSH.ConnectTimeout.Duration,
		})
		if err != nil {
			return nil, nil, err
		}
		return c, c, nil
	default:
		return nil, nil, fmt.Errorf("%w: unsupported target type %q", domain.ErrInvalid, t.Type)
	}
}

// secretCache resolves each app's secrets at most once per Plan.
type secretCache struct {
	resolver secrets.Resolver
	byApp    map[string]resolvedApp
	values   []string
}

type resolvedApp struct {
	spec domain.ContainerSpec
	err  error
}

func (c *secretCache) spec(app config.App) (domain.ContainerSpec, error) {
	if r, ok := c.byApp[app.Name]; ok {
		return r.spec, r.err
	}
	spec, vals, err := c.resolver.Resolve(app)
	c.byApp[app.Name] = resolvedApp{spec, err}
	c.values = append(c.values, vals...)
	return spec, err
}

func (s *Session) planTarget(ctx context.Context, t config.Target, sc *secretCache) *TargetRun {
	tr := &TargetRun{Target: t, closer: nopCloser{}}
	// Resolve secrets first: it is local and cheap, and a deploy that cannot
	// be fully specified should not even open a connection.
	specs := make([]domain.ContainerSpec, 0, len(t.Apps))
	var specErrs []error
	for _, name := range t.Apps {
		app, _ := s.cfg.App(name) // existence is guaranteed by config validation
		spec, err := sc.spec(app)
		if err != nil {
			specErrs = append(specErrs, err)
			continue
		}
		specs = append(specs, spec)
	}
	if err := errors.Join(specErrs...); err != nil {
		tr.Err = err
		return tr
	}
	exec, closer, err := executorFor(t)
	if err != nil {
		tr.Err = err
		return tr
	}
	tr.closer = closer
	rt, err := s.reg.New(t.Runtime.Driver, runtime.Deps{Exec: exec, Options: t.Runtime.Options})
	if err != nil {
		tr.Err = err
		return tr
	}
	if _, err := rt.Ping(ctx); err != nil {
		tr.Err = fmt.Errorf("runtime %s: %w", t.Runtime.Driver, err)
		return tr
	}
	name := t.Name
	tr.engine = deploy.New(rt, deploy.Options{
		Settle:      s.cfg.Deploy.Settle.Duration,
		StopTimeout: s.cfg.Deploy.StopTimeout.Duration,
		OnEvent:     func(ev deploy.Event) { s.emit(name, ev) },
	})
	tr.Plan, tr.Err = tr.engine.Plan(ctx, specs)
	return tr
}
