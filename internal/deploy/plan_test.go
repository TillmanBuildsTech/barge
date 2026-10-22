package deploy_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/TillmanBuildsTech/barge/internal/deploy"
	"github.com/TillmanBuildsTech/barge/internal/domain"
)

func TestPlanEmptyRuntimeCreatesEverythingInOrder(t *testing.T) {
	e := newEngine(newSpy())
	plan, err := e.Plan(t.Context(), []domain.ContainerSpec{spec("web", "nginx:1"), spec("api", "api:1")})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 2 || plan.Items[0].Spec.Name != "web" || plan.Items[1].Spec.Name != "api" {
		t.Fatalf("items = %+v", plan.Items)
	}
	for _, it := range plan.Items {
		if it.Action != deploy.ActionCreate || it.Reason == "" {
			t.Errorf("%s: action=%v reason=%q", it.Spec.Name, it.Action, it.Reason)
		}
	}
	if !plan.HasChanges() || plan.HasBlocked() {
		t.Errorf("HasChanges=%v HasBlocked=%v", plan.HasChanges(), plan.HasBlocked())
	}
}

func TestPlanIsReadOnly(t *testing.T) {
	rt := newSpy()
	e := newEngine(rt)
	mustApply(t, e, spec("web", "nginx:1"))
	rt.reset()
	if _, err := e.Plan(t.Context(), []domain.ContainerSpec{spec("web", "nginx:2"), spec("new", "x:1")}); err != nil {
		t.Fatal(err)
	}
	for _, c := range rt.mutations() {
		t.Errorf("Plan performed a mutating call: %s", c)
	}
}

func TestPlanActions(t *testing.T) {
	rt := newSpy()
	e := newEngine(rt)
	mustApply(t, e, spec("same", "i:1"), spec("changed", "i:1"), spec("stopped", "i:1"))
	if err := rt.Runtime.Stop(t.Context(), "stopped", 0); err != nil {
		t.Fatal(err)
	}

	plan, err := e.Plan(t.Context(), []domain.ContainerSpec{
		spec("same", "i:1"), spec("changed", "i:2"), spec("stopped", "i:1"), spec("fresh", "i:1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []deploy.Action{deploy.ActionNoop, deploy.ActionReplace, deploy.ActionStart, deploy.ActionCreate}
	for i, it := range plan.Items {
		if it.Action != want[i] {
			t.Errorf("%s: action = %v, want %v (%s)", it.Spec.Name, it.Action, want[i], it.Reason)
		}
	}
}

func TestPlanReasonsNeverLeakEnvValues(t *testing.T) {
	rt := newSpy()
	e := newEngine(rt)
	mustApply(t, e, spec("web", "i:1"))
	changed := spec("web", "i:1")
	changed.Env = map[string]string{"TOKEN": "rotated-secret"}
	plan, err := e.Plan(t.Context(), []domain.ContainerSpec{changed})
	if err != nil {
		t.Fatal(err)
	}
	it := plan.Items[0]
	if it.Action != deploy.ActionReplace {
		t.Fatalf("an env change must trigger a replace, got %v", it.Action)
	}
	for _, s := range []string{it.Reason, plan.String()} {
		if strings.Contains(s, "rotated-secret") || strings.Contains(s, "hunter2") {
			t.Errorf("secret leaked: %q", s)
		}
	}
}

func TestPlanBlocksUnmanagedContainerWithSameName(t *testing.T) {
	rt := newSpy()
	// Created behind barge's back: no barge labels.
	if _, err := rt.Runtime.Create(t.Context(), spec("web", "other:1")); err != nil {
		t.Fatal(err)
	}
	plan, err := newEngine(rt).Plan(t.Context(), []domain.ContainerSpec{spec("web", "nginx:1")})
	if err != nil {
		t.Fatal(err)
	}
	it := plan.Items[0]
	if it.Action != deploy.ActionBlocked || !errors.Is(it.Err, domain.ErrConflict) || !strings.Contains(it.Reason, "not managed by barge") {
		t.Errorf("item = %+v", it)
	}
	if !plan.HasBlocked() {
		t.Error("HasBlocked")
	}
}

func TestPlanBlocksManagedContainerOfAnotherApp(t *testing.T) {
	rt := newSpy()
	other := spec("web", "x:1")
	other.Labels = map[string]string{domain.LabelManagedBy: domain.ManagedByValue, domain.LabelApp: "someone-else"}
	if _, err := rt.Runtime.Create(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	plan, _ := newEngine(rt).Plan(t.Context(), []domain.ContainerSpec{spec("web", "nginx:1")})
	if plan.Items[0].Action != deploy.ActionBlocked {
		t.Errorf("action = %v", plan.Items[0].Action)
	}
}

func TestPlanBlocksOnInterruptedPreviousDeployment(t *testing.T) {
	rt := newSpy()
	e := newEngine(rt)
	mustApply(t, e, spec("web", "i:1"))
	// Simulate a crash mid-swap: the old container was renamed away.
	if err := rt.Runtime.Rename(t.Context(), "web", "web-barge-old"); err != nil {
		t.Fatal(err)
	}
	plan, err := e.Plan(t.Context(), []domain.ContainerSpec{spec("web", "i:1")})
	if err != nil {
		t.Fatal(err)
	}
	it := plan.Items[0]
	if it.Action != deploy.ActionBlocked || !strings.Contains(it.Reason, "web-barge-old") || !strings.Contains(it.Reason, "interrupted") {
		t.Errorf("item = %+v", it)
	}
}

func TestPlanRejectsInvalidSpecsAllAtOnce(t *testing.T) {
	bad1 := spec("web", "--privileged")
	bad2 := spec("bad name", "ok:1")
	withReserved := spec("r", "ok:1")
	withReserved.Labels = map[string]string{"barge.app": "spoof"}
	_, err := newEngine(newSpy()).Plan(t.Context(), []domain.ContainerSpec{bad1, bad2, withReserved})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	for _, want := range []string{"--privileged", "bad name", "reserved"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
}

func TestPlanRejectsDuplicateNames(t *testing.T) {
	_, err := newEngine(newSpy()).Plan(t.Context(), []domain.ContainerSpec{spec("web", "a:1"), spec("web", "b:1")})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
}

func TestPlanRuntimeFailureIsAnError(t *testing.T) {
	rt := newSpy()
	rt.fail["Inspect:web"] = domain.ErrUnavailable
	if _, err := newEngine(rt).Plan(t.Context(), []domain.ContainerSpec{spec("web", "a:1")}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestPlanStringIsHumanReadable(t *testing.T) {
	rt := newSpy()
	e := newEngine(rt)
	mustApply(t, e, spec("keep", "i:1"))
	plan, _ := e.Plan(t.Context(), []domain.ContainerSpec{spec("keep", "i:1"), spec("new", "i:1")})
	out := plan.String()
	for _, want := range []string{"keep", "new", "create", "unchanged"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan output missing %q:\n%s", want, out)
		}
	}
}
