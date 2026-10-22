package deploy_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TillmanBuildsTech/barge/internal/deploy"
	"github.com/TillmanBuildsTech/barge/internal/domain"
	"github.com/TillmanBuildsTech/barge/internal/runtime"
	"github.com/TillmanBuildsTech/barge/internal/runtime/fake"
)

// spy wraps a runtime to record calls and inject faults. Like a real runtime
// it fails every call once the context is cancelled, which is what makes the
// "Ctrl-C mid-deploy" rollback test meaningful.
type spy struct {
	runtime.Runtime

	mu    sync.Mutex
	calls []string
	// fail maps "Method:arg" to an error returned once, then forgotten.
	fail map[string]error
	// hook runs (once) just before the matching call proceeds.
	hook map[string]func()
	// crashImages: containers of these images report Exited once inspected.
	crashImages map[string]bool
}

func newSpy() *spy {
	return &spy{Runtime: fake.New(), fail: map[string]error{}, hook: map[string]func(){}, crashImages: map[string]bool{}}
}

func (s *spy) enter(ctx context.Context, key string) error {
	s.mu.Lock()
	s.calls = append(s.calls, key)
	h := s.hook[key]
	delete(s.hook, key)
	s.mu.Unlock()
	if h != nil {
		h()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err, ok := s.fail[key]; ok {
		delete(s.fail, key)
		return err
	}
	return nil
}

func (s *spy) Pull(ctx context.Context, ref string) error {
	if err := s.enter(ctx, "Pull:"+ref); err != nil {
		return err
	}
	return s.Runtime.Pull(ctx, ref)
}

func (s *spy) Create(ctx context.Context, sp domain.ContainerSpec) (string, error) {
	if err := s.enter(ctx, "Create:"+sp.Name); err != nil {
		return "", err
	}
	return s.Runtime.Create(ctx, sp)
}

func (s *spy) Start(ctx context.Context, id string) error {
	if err := s.enter(ctx, "Start:"+id); err != nil {
		return err
	}
	return s.Runtime.Start(ctx, id)
}

func (s *spy) Stop(ctx context.Context, id string, d time.Duration) error {
	if err := s.enter(ctx, "Stop:"+id); err != nil {
		return err
	}
	return s.Runtime.Stop(ctx, id, d)
}

func (s *spy) Remove(ctx context.Context, id string, force bool) error {
	if err := s.enter(ctx, "Remove:"+id); err != nil {
		return err
	}
	return s.Runtime.Remove(ctx, id, force)
}

func (s *spy) Rename(ctx context.Context, id, to string) error {
	if err := s.enter(ctx, "Rename:"+id+">"+to); err != nil {
		return err
	}
	return s.Runtime.Rename(ctx, id, to)
}

func (s *spy) Inspect(ctx context.Context, id string) (domain.ContainerState, error) {
	if err := s.enter(ctx, "Inspect:"+id); err != nil {
		return domain.ContainerState{}, err
	}
	st, err := s.Runtime.Inspect(ctx, id)
	if err == nil && s.crashImages[st.Image] && st.Status == domain.StatusRunning {
		st.Status = domain.StatusExited
	}
	return st, err
}

// mutations returns the state-changing calls, in order.
func (s *spy) mutations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.calls {
		if !strings.HasPrefix(c, "Inspect:") {
			out = append(out, c)
		}
	}
	return out
}

func (s *spy) reset() { s.mu.Lock(); s.calls = nil; s.mu.Unlock() }

func (s *spy) containers(t *testing.T) []domain.ContainerState {
	t.Helper()
	cs, err := s.List(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func spec(name, image string) domain.ContainerSpec {
	return domain.ContainerSpec{
		Name: name, Image: image, Command: []string{"sleep", "300"},
		Env: map[string]string{"TOKEN": "hunter2"},
	}
}

func newEngine(rt runtime.Runtime) *deploy.Engine {
	return deploy.New(rt, deploy.Options{Settle: 30 * time.Millisecond, PollInterval: 5 * time.Millisecond, StopTimeout: time.Second})
}

// mustApply plans and applies, failing the test on engine-level errors.
func mustApply(t *testing.T, e *deploy.Engine, specs ...domain.ContainerSpec) []deploy.Result {
	t.Helper()
	plan, err := e.Plan(t.Context(), specs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	res, err := e.Apply(t.Context(), plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return res
}

func names(cs []domain.ContainerState) []string {
	n := make([]string, 0, len(cs))
	for _, c := range cs {
		n = append(n, c.Name)
	}
	slices.Sort(n)
	return n
}
