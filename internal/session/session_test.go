package session_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TillmanBuildsTech/barge/internal/config"
	"github.com/TillmanBuildsTech/barge/internal/deploy"
	"github.com/TillmanBuildsTech/barge/internal/domain"
	rt "github.com/TillmanBuildsTech/barge/internal/runtime"
	"github.com/TillmanBuildsTech/barge/internal/runtime/fake"
	"github.com/TillmanBuildsTech/barge/internal/session"
)

const cfgYAML = `
version: 1
runtime: {driver: fake}
deploy: {settle: 10ms}
apps:
  - name: web
    image: nginx:1
    secrets:
      TOKEN: {env: BARGE_SESSION_TOKEN}
  - name: api
    image: api:1
targets:
  - {name: a, type: local, apps: [web]}
  - {name: b, type: local, apps: [api]}
`

func setup(t *testing.T, yaml string) (*config.Config, *rt.Registry, *fake.Runtime) {
	t.Helper()
	cfg, err := config.Load(strings.NewReader(yaml))
	if err != nil {
		t.Fatal(err)
	}
	f := fake.New()
	reg := rt.NewRegistry()
	if err := reg.Register("fake", func(rt.Deps) (rt.Runtime, error) { return f, nil }); err != nil {
		t.Fatal(err)
	}
	return cfg, reg, f
}

func TestSelectTargets(t *testing.T) {
	cfg, _, _ := setup(t, cfgYAML)
	all, err := session.SelectTargets(cfg, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("all: %v %v", all, err)
	}
	one, err := session.SelectTargets(cfg, "b")
	if err != nil || len(one) != 1 || one[0].Name != "b" {
		t.Fatalf("one: %v %v", one, err)
	}
	if _, err := session.SelectTargets(cfg, "zzz"); err == nil || !strings.Contains(err.Error(), "a, b") {
		t.Errorf("unknown target should list the known ones: %v", err)
	}
}

func TestPlanThenApplyEmitsTaggedEventsInOrder(t *testing.T) {
	t.Setenv("BARGE_SESSION_TOKEN", "tok-value-12345")
	cfg, reg, f := setup(t, cfgYAML)
	s := session.New(cfg, reg, cfg.Targets)
	defer func() { _ = s.Close() }()

	runs := s.Plan(t.Context())
	if len(runs) != 2 || !s.OK() || s.Changes() != 2 {
		t.Fatalf("runs=%d ok=%v changes=%d", len(runs), s.OK(), s.Changes())
	}
	var seen []string
	if err := s.Apply(t.Context(), func(ev session.Event) {
		if ev.Kind == deploy.EventDone {
			seen = append(seen, fmt.Sprintf("%s/%s/%s", ev.Target, ev.App, ev.Result.Outcome))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(seen, ","); got != "a/web/applied,b/api/applied" {
		t.Errorf("done events = %q", got)
	}
	cs, _ := f.List(t.Context(), nil)
	if len(cs) != 2 {
		t.Errorf("containers = %v", cs)
	}
	for _, r := range runs {
		if !r.Applied || len(r.Results) != 1 || !r.OK() {
			t.Errorf("run %s: %+v", r.Target.Name, r)
		}
	}
}

func TestReplanAfterApplyShowsNothingToDo(t *testing.T) {
	t.Setenv("BARGE_SESSION_TOKEN", "tok-value-12345")
	cfg, reg, _ := setup(t, cfgYAML)
	s := session.New(cfg, reg, cfg.Targets)
	defer func() { _ = s.Close() }()
	s.Plan(t.Context())
	if err := s.Apply(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	runs := s.Plan(t.Context())
	if s.Changes() != 0 || !s.OK() {
		t.Errorf("after apply, a re-plan must be clean: changes=%d ok=%v", s.Changes(), s.OK())
	}
	for _, r := range runs {
		if r.Applied || r.Results != nil {
			t.Errorf("a re-plan must start from a fresh state: %+v", r)
		}
	}
}

func TestApplyRefusesAndChangesNothingWhenAnyTargetHasAProblem(t *testing.T) {
	os.Unsetenv("BARGE_SESSION_TOKEN") //nolint:errcheck
	cfg, reg, f := setup(t, cfgYAML)
	s := session.New(cfg, reg, cfg.Targets)
	defer func() { _ = s.Close() }()
	s.Plan(t.Context())
	if s.OK() {
		t.Fatal("target a has an unresolvable secret; the session must not be OK")
	}
	err := s.Apply(t.Context(), nil)
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("want a refusal, got %v", err)
	}
	if cs, _ := f.List(t.Context(), nil); len(cs) != 0 {
		t.Errorf("fail closed: even healthy target b must not be deployed: %v", cs)
	}
}

func TestApplyBeforePlanIsRefused(t *testing.T) {
	cfg, reg, _ := setup(t, cfgYAML)
	s := session.New(cfg, reg, cfg.Targets)
	defer func() { _ = s.Close() }()
	if err := s.Apply(t.Context(), nil); err == nil {
		t.Fatal("applying without a plan must be refused")
	}
}

func TestRedactorKnowsSecretsFromEveryPlan(t *testing.T) {
	cfg, reg, _ := setup(t, cfgYAML)
	s := session.New(cfg, reg, cfg.Targets)
	defer func() { _ = s.Close() }()

	t.Setenv("BARGE_SESSION_TOKEN", "first-secret-value")
	s.Plan(t.Context())
	t.Setenv("BARGE_SESSION_TOKEN", "second-secret-value")
	s.Plan(t.Context())

	got := s.Redactor().Redact("a first-secret-value b second-secret-value c")
	if strings.Contains(got, "secret-value") {
		t.Errorf("both generations of the secret must stay redacted: %q", got)
	}
}

func TestSecretFileIsReReadOnEachPlan(t *testing.T) {
	f := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(f, []byte("v1-secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	y := strings.Replace(cfgYAML, "{env: BARGE_SESSION_TOKEN}", "{file: "+f+"}", 1)
	cfg, reg, _ := setup(t, y)
	s := session.New(cfg, reg, cfg.Targets)
	defer func() { _ = s.Close() }()
	s.Plan(t.Context())
	if err := s.Apply(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte("v2-secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Plan(t.Context())
	if s.Changes() != 1 {
		t.Errorf("a changed secret file must show up on re-plan, changes=%d", s.Changes())
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	cfg, reg, _ := setup(t, cfgYAML)
	s := session.New(cfg, reg, cfg.Targets)
	s.Plan(t.Context())
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

var _ = domain.ErrInvalid
