package deploy_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/TillmanBuildsTech/barge/internal/deploy"
	"github.com/TillmanBuildsTech/barge/internal/domain"
)

// leftover plants a barge-owned "<name>-barge-old", as a replace whose cleanup
// failed would leave behind.
func leftover(t *testing.T, rt *spy, owned bool) {
	const name = "web"
	t.Helper()
	s := spec(name+"-barge-old", "old:1")
	if owned {
		s.Labels = map[string]string{domain.LabelManagedBy: domain.ManagedByValue, domain.LabelApp: name}
	}
	if _, err := rt.Runtime.Create(t.Context(), s); err != nil {
		t.Fatal(err)
	}
}

func TestLeftoverPreviousVersionNextToHealthyCurrentIsCleanedUp(t *testing.T) {
	rt := newSpy()
	e := newEngine(rt)
	mustApply(t, e, spec("web", "nginx:1"))
	leftover(t, rt, true)

	plan, err := e.Plan(t.Context(), []domain.ContainerSpec{spec("web", "nginx:1")})
	if err != nil {
		t.Fatal(err)
	}
	it := plan.Items[0]
	if it.Action != deploy.ActionCleanup || !strings.Contains(it.Reason, "web-barge-old") || plan.HasBlocked() || !plan.HasChanges() {
		t.Fatalf("item = %+v", it)
	}

	rt.reset()
	res, err := e.Apply(t.Context(), plan)
	if err != nil || res[0].Outcome != deploy.OutcomeApplied || res[0].Action != deploy.ActionCleanup {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if got, want := rt.mutations(), []string{"Remove:web-barge-old"}; !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %v, want only %v (the running container must not be touched)", got, want)
	}
	if cs := rt.containers(t); len(cs) != 1 || cs[0].Name != "web" || cs[0].Status != domain.StatusRunning {
		t.Errorf("state = %+v", cs)
	}
	if again, _ := e.Plan(t.Context(), []domain.ContainerSpec{spec("web", "nginx:1")}); again.HasChanges() {
		t.Error("after cleanup the plan must be clean")
	}
}

func TestLeftoverWithChangedSpecIsReplacedAndTheLeftoverCleared(t *testing.T) {
	rt := newSpy()
	e := newEngine(rt)
	mustApply(t, e, spec("web", "nginx:1"))
	leftover(t, rt, true)

	plan, _ := e.Plan(t.Context(), []domain.ContainerSpec{spec("web", "nginx:2")})
	if plan.Items[0].Action != deploy.ActionReplace {
		t.Fatalf("item = %+v", plan.Items[0])
	}
	res, err := e.Apply(t.Context(), plan)
	if err != nil || res[0].Outcome != deploy.OutcomeApplied {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if cs := rt.containers(t); len(cs) != 1 || cs[0].Image != "nginx:2" {
		t.Errorf("state = %v", names(cs))
	}
}

func TestLeftoverStaysBlockedWhenItCannotBeProvenSafe(t *testing.T) {
	cases := map[string]func(t *testing.T, rt *spy){
		"leftover not owned by barge": func(t *testing.T, rt *spy) { leftover(t, rt, false) },
		"current container stopped": func(t *testing.T, rt *spy) {
			leftover(t, rt, true)
			_ = rt.Runtime.Stop(t.Context(), "web", 0)
		},
		"current container missing (crashed mid-swap)": func(t *testing.T, rt *spy) {
			leftover(t, rt, true)
			_ = rt.Runtime.Remove(t.Context(), "web", true)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			rt := newSpy()
			e := newEngine(rt)
			mustApply(t, e, spec("web", "nginx:1"))
			setup(t, rt)
			plan, err := e.Plan(t.Context(), []domain.ContainerSpec{spec("web", "nginx:1")})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Items[0].Action != deploy.ActionBlocked || !strings.Contains(plan.Items[0].Reason, "web-barge-old") {
				t.Errorf("item = %+v", plan.Items[0])
			}
		})
	}
}

func TestFailedLeftoverCleanupIsAFailureNotASilentSuccess(t *testing.T) {
	rt := newSpy()
	e := newEngine(rt)
	mustApply(t, e, spec("web", "nginx:1"))
	leftover(t, rt, true)
	rt.fail["Remove:web-barge-old"] = errors.New("device busy")
	res := mustApply(t, e, spec("web", "nginx:1"))
	if res[0].Outcome != deploy.OutcomeFailed || res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "web-barge-old") {
		t.Errorf("res = %+v", res[0])
	}
}

func TestCleanupEmitsEvents(t *testing.T) {
	rt := newSpy()
	mustApply(t, newEngine(rt), spec("web", "nginx:1"))
	leftover(t, rt, true)
	rec := &recorded{}
	mustApply(t, engineWithEvents(rt, rec), spec("web", "nginx:1"))
	want := []string{"start:web:cleanup", "step:web:remove leftover previous version", "done:web:applied"}
	if !reflect.DeepEqual(rec.log, want) {
		t.Errorf("events = %q, want %q", rec.log, want)
	}
}

func TestEngineRejectsNamesThatCollideWithItsTemporaryContainers(t *testing.T) {
	for _, n := range []string{"web-barge-old", "web-barge-new"} {
		_, err := newEngine(newSpy()).Plan(t.Context(), []domain.ContainerSpec{spec(n, "x:1")})
		if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "-barge-") {
			t.Errorf("%s: want ErrInvalid mentioning the reserved suffix, got %v", n, err)
		}
	}
}
