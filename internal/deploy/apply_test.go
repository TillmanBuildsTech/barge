package deploy_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/TillmanBuildsTech/barge/internal/deploy"
	"github.com/TillmanBuildsTech/barge/internal/domain"
)

func TestApplyCreatesLabelsAndStarts(t *testing.T) {
	rt := newSpy()
	res := mustApply(t, newEngine(rt), spec("web", "nginx:1"))

	if len(res) != 1 || res[0].Outcome != deploy.OutcomeApplied || res[0].Err != nil || res[0].App != "web" {
		t.Fatalf("result = %+v", res)
	}
	cs := rt.containers(t)
	if len(cs) != 1 {
		t.Fatalf("containers = %v", names(cs))
	}
	c := cs[0]
	want := spec("web", "nginx:1")
	if c.Status != domain.StatusRunning || c.Name != "web" {
		t.Errorf("state = %+v", c)
	}
	if c.Labels[domain.LabelManagedBy] != domain.ManagedByValue || c.Labels[domain.LabelApp] != "web" || c.Labels[domain.LabelSpecHash] != want.Hash() {
		t.Errorf("labels = %v", c.Labels)
	}
	if want := []string{"Pull:nginx:1", "Create:web", "Start:web"}; !reflect.DeepEqual(rt.mutations(), want) {
		t.Errorf("calls = %v", rt.mutations())
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	rt := newSpy()
	e := newEngine(rt)
	mustApply(t, e, spec("web", "nginx:1"))
	rt.reset()
	res := mustApply(t, e, spec("web", "nginx:1"))
	if res[0].Outcome != deploy.OutcomeUnchanged {
		t.Errorf("outcome = %v", res[0].Outcome)
	}
	if m := rt.mutations(); len(m) != 0 {
		t.Errorf("second apply must change nothing, did: %v", m)
	}
}

func TestApplyStartsStoppedContainerWithoutRecreating(t *testing.T) {
	rt := newSpy()
	e := newEngine(rt)
	mustApply(t, e, spec("web", "nginx:1"))
	before := rt.containers(t)[0].ID
	_ = rt.Runtime.Stop(t.Context(), "web", 0)
	rt.reset()

	res := mustApply(t, e, spec("web", "nginx:1"))
	if res[0].Outcome != deploy.OutcomeApplied {
		t.Fatalf("outcome = %v", res[0].Outcome)
	}
	after := rt.containers(t)
	if len(after) != 1 || after[0].ID != before || after[0].Status != domain.StatusRunning {
		t.Errorf("after = %+v", after)
	}
	if want := []string{"Start:web"}; !reflect.DeepEqual(rt.mutations(), want) {
		t.Errorf("calls = %v, want %v", rt.mutations(), want)
	}
}

func TestApplyReplaceOrderAndCleanState(t *testing.T) {
	rt := newSpy()
	e := newEngine(rt)
	mustApply(t, e, spec("web", "nginx:1"))
	rt.reset()

	res := mustApply(t, e, spec("web", "nginx:2"))
	if res[0].Outcome != deploy.OutcomeApplied || res[0].Action != deploy.ActionReplace {
		t.Fatalf("result = %+v", res[0])
	}
	want := []string{
		"Pull:nginx:2",
		"Create:web-barge-new",
		"Stop:web",
		"Rename:web>web-barge-old",
		"Rename:web-barge-new>web",
		"Start:web",
		"Remove:web-barge-old",
	}
	if got := rt.mutations(); !reflect.DeepEqual(got, want) {
		t.Errorf("replace order\n got: %v\nwant: %v", got, want)
	}
	cs := rt.containers(t)
	if len(cs) != 1 || cs[0].Name != "web" || cs[0].Image != "nginx:2" || cs[0].Status != domain.StatusRunning {
		t.Errorf("final state = %+v", cs)
	}
	if cs[0].Labels[domain.LabelSpecHash] != spec("web", "nginx:2").Hash() {
		t.Error("spec hash label not updated")
	}
}

// The heart of the safety story: whichever step of a replace fails, the
// previous version must be left running under its original name, with no
// leftover temp containers.
func TestApplyReplaceFailureAlwaysRestoresPreviousVersion(t *testing.T) {
	steps := []struct {
		name     string
		key      string // injected failure
		crashNew bool
		rolled   bool // expect an actual rollback to have been needed
	}{
		{"pull fails", "Pull:nginx:2", false, false},
		{"create fails", "Create:web-barge-new", false, false},
		{"stop fails", "Stop:web", false, true},
		{"rename old away fails", "Rename:web>web-barge-old", false, true},
		{"rename new into place fails", "Rename:web-barge-new>web", false, true},
		{"start fails", "Start:web", false, true},
		{"new version crashes during settle", "", true, true},
	}
	for _, tt := range steps {
		t.Run(tt.name, func(t *testing.T) {
			rt := newSpy()
			e := newEngine(rt)
			mustApply(t, e, spec("web", "nginx:1"))
			if tt.key != "" {
				rt.fail[tt.key] = errors.New("injected failure")
			}
			if tt.crashNew {
				rt.crashImages["nginx:2"] = true
			}

			res := mustApply(t, e, spec("web", "nginx:2"))
			r := res[0]
			if r.Outcome != deploy.OutcomeFailed || r.Err == nil {
				t.Fatalf("want Failed with error, got %+v", r)
			}
			if r.RollbackErr != nil {
				t.Fatalf("rollback must succeed here: %v", r.RollbackErr)
			}
			if r.RolledBack != tt.rolled {
				t.Errorf("RolledBack = %v, want %v", r.RolledBack, tt.rolled)
			}
			cs := rt.containers(t)
			if len(cs) != 1 {
				t.Fatalf("leftover containers: %v", names(cs))
			}
			c := cs[0]
			if c.Name != "web" || c.Image != "nginx:1" || c.Status != domain.StatusRunning {
				t.Errorf("previous version not restored: %+v", c)
			}
			if c.Labels[domain.LabelSpecHash] != spec("web", "nginx:1").Hash() {
				t.Error("restored container lost its original spec hash")
			}
		})
	}
}

func TestApplyReplaceFailureKeepsStoppedPreviousVersionStopped(t *testing.T) {
	rt := newSpy()
	e := newEngine(rt)
	mustApply(t, e, spec("web", "nginx:1"))
	_ = rt.Runtime.Stop(t.Context(), "web", 0) // operator stopped it on purpose
	rt.fail["Start:web"] = errors.New("injected failure")

	res := mustApply(t, e, spec("web", "nginx:2"))
	if res[0].Outcome != deploy.OutcomeFailed {
		t.Fatalf("outcome = %v", res[0].Outcome)
	}
	cs := rt.containers(t)
	if len(cs) != 1 || cs[0].Image != "nginx:1" || cs[0].Status == domain.StatusRunning {
		t.Errorf("rollback must restore the previous state (stopped), got %+v", cs)
	}
}

func TestApplyRollbackFailureIsReportedLoudly(t *testing.T) {
	rt := newSpy()
	e := newEngine(rt)
	mustApply(t, e, spec("web", "nginx:1"))
	rt.fail["Start:web"] = errors.New("start failed")
	rt.fail["Rename:web-barge-old>web"] = errors.New("rename back failed")

	res := mustApply(t, e, spec("web", "nginx:2"))
	r := res[0]
	if r.Outcome != deploy.OutcomeFailed || r.Err == nil || r.RollbackErr == nil {
		t.Fatalf("want both Err and RollbackErr, got %+v", r)
	}
	if !strings.Contains(r.RollbackErr.Error(), "web-barge-old") {
		t.Errorf("recovery message should name the container to recover: %v", r.RollbackErr)
	}
}

func TestApplyRollsBackEvenWhenContextIsCancelledMidDeploy(t *testing.T) {
	rt := newSpy()
	e := newEngine(rt)
	mustApply(t, e, spec("web", "nginx:1"))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// Ctrl-C arrives just as the new version is about to start.
	rt.hook["Start:web"] = cancel

	plan, err := e.Plan(t.Context(), []domain.ContainerSpec{spec("web", "nginx:2")})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Apply(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Outcome != deploy.OutcomeFailed || !errors.Is(res[0].Err, context.Canceled) {
		t.Fatalf("result = %+v", res[0])
	}
	cs := rt.containers(t)
	if len(cs) != 1 || cs[0].Image != "nginx:1" || cs[0].Status != domain.StatusRunning || cs[0].Name != "web" {
		t.Errorf("a cancelled deploy must still restore the old version: %+v", cs)
	}
}

func TestApplyCreateFailureLeavesNothingBehind(t *testing.T) {
	for name, tt := range map[string]struct {
		key   string
		crash bool
	}{
		"start fails":      {key: "Start:web"},
		"crashes at start": {crash: true},
	} {
		t.Run(name, func(t *testing.T) {
			rt := newSpy()
			if tt.key != "" {
				rt.fail[tt.key] = errors.New("injected")
			}
			if tt.crash {
				rt.crashImages["bad:1"] = true
			}
			res := mustApply(t, newEngine(rt), spec("web", "bad:1"))
			if res[0].Outcome != deploy.OutcomeFailed || res[0].Err == nil {
				t.Fatalf("result = %+v", res[0])
			}
			if cs := rt.containers(t); len(cs) != 0 {
				t.Errorf("a failed first deploy must not leave a broken container: %v", names(cs))
			}
		})
	}
}

func TestApplyCleansStaleTempContainerFromEarlierCrash(t *testing.T) {
	rt := newSpy()
	e := newEngine(rt)
	mustApply(t, e, spec("web", "nginx:1"))
	stale := spec("web-barge-new", "junk:1")
	stale.Labels = map[string]string{domain.LabelManagedBy: domain.ManagedByValue, domain.LabelApp: "web"}
	if _, err := rt.Runtime.Create(t.Context(), stale); err != nil {
		t.Fatal(err)
	}
	res := mustApply(t, e, spec("web", "nginx:2"))
	if res[0].Outcome != deploy.OutcomeApplied {
		t.Fatalf("result = %+v", res[0])
	}
	if cs := rt.containers(t); len(cs) != 1 || cs[0].Image != "nginx:2" {
		t.Errorf("state = %v", names(cs))
	}
}

func TestApplyDoesNotTouchUnmanagedTempNameSquatter(t *testing.T) {
	rt := newSpy()
	e := newEngine(rt)
	mustApply(t, e, spec("web", "nginx:1"))
	// Someone else's container happens to use the temp name: it has no barge labels.
	if _, err := rt.Runtime.Create(t.Context(), spec("web-barge-new", "theirs:1")); err != nil {
		t.Fatal(err)
	}
	res := mustApply(t, e, spec("web", "nginx:2"))
	if res[0].Outcome != deploy.OutcomeFailed || !errors.Is(res[0].Err, domain.ErrConflict) {
		t.Fatalf("want Failed/ErrConflict, got %+v", res[0])
	}
	found := false
	for _, c := range rt.containers(t) {
		if c.Name == "web-barge-new" && c.Image == "theirs:1" {
			found = true
		}
	}
	if !found {
		t.Error("barge deleted a container it does not own")
	}
}

func TestApplyContinuesAfterOneAppFails(t *testing.T) {
	rt := newSpy()
	rt.fail["Pull:bad:1"] = errors.New("registry down")
	res := mustApply(t, newEngine(rt), spec("a", "ok:1"), spec("b", "bad:1"), spec("c", "ok:1"))
	got := []deploy.Outcome{res[0].Outcome, res[1].Outcome, res[2].Outcome}
	want := []deploy.Outcome{deploy.OutcomeApplied, deploy.OutcomeFailed, deploy.OutcomeApplied}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("outcomes = %v, want %v", got, want)
	}
	if res[0].App != "a" || res[1].App != "b" || res[2].App != "c" {
		t.Errorf("results out of order: %+v", res)
	}
}

func TestApplyRefusesBlockedPlanAndChangesNothing(t *testing.T) {
	rt := newSpy()
	if _, err := rt.Runtime.Create(t.Context(), spec("web", "unmanaged:1")); err != nil {
		t.Fatal(err)
	}
	e := newEngine(rt)
	plan, err := e.Plan(t.Context(), []domain.ContainerSpec{spec("fine", "ok:1"), spec("web", "nginx:1")})
	if err != nil {
		t.Fatal(err)
	}
	rt.reset()
	if _, err := e.Apply(t.Context(), plan); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	if m := rt.mutations(); len(m) != 0 {
		t.Errorf("a blocked plan must change nothing, but: %v", m)
	}
}

func TestApplyCancelledBeforeStartSkipsEverything(t *testing.T) {
	rt := newSpy()
	e := newEngine(rt)
	plan, err := e.Plan(t.Context(), []domain.ContainerSpec{spec("a", "x:1"), spec("b", "x:1")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	res, err := e.Apply(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Outcome != deploy.OutcomeSkipped {
			t.Errorf("%s: outcome = %v, want Skipped", r.App, r.Outcome)
		}
	}
	if len(rt.containers(t)) != 0 {
		t.Error("nothing should have been created")
	}
}

func TestApplySkipsRemainingAppsAfterCancellation(t *testing.T) {
	rt := newSpy()
	e := newEngine(rt)
	plan, _ := e.Plan(t.Context(), []domain.ContainerSpec{spec("a", "x:1"), spec("b", "x:1")})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	rt.hook["Start:a"] = cancel // Ctrl-C while the first app is starting
	res, err := e.Apply(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Outcome != deploy.OutcomeFailed || res[1].Outcome != deploy.OutcomeSkipped {
		t.Errorf("outcomes = %v, %v", res[0].Outcome, res[1].Outcome)
	}
	if len(rt.containers(t)) != 0 {
		t.Errorf("leftovers: %v", names(rt.containers(t)))
	}
}

func TestApplyWarnsWhenOldContainerCannotBeRemoved(t *testing.T) {
	rt := newSpy()
	e := newEngine(rt)
	mustApply(t, e, spec("web", "nginx:1"))
	rt.fail["Remove:web-barge-old"] = errors.New("device busy")
	res := mustApply(t, e, spec("web", "nginx:2"))
	r := res[0]
	if r.Outcome != deploy.OutcomeApplied || len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "web-barge-old") {
		t.Errorf("a failed cleanup must not fail the deploy but must warn: %+v", r)
	}
}
