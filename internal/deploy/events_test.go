package deploy_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/TillmanBuildsTech/barge/internal/deploy"
	"github.com/TillmanBuildsTech/barge/internal/domain"
	"github.com/TillmanBuildsTech/barge/internal/runtime"
)

type recorded struct {
	mu  sync.Mutex
	log []string
	evs []deploy.Event
}

func (r *recorded) on(ev deploy.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, ev)
	switch ev.Kind {
	case deploy.EventStart:
		r.log = append(r.log, fmt.Sprintf("start:%s:%s", ev.App, ev.Action))
	case deploy.EventStep:
		r.log = append(r.log, fmt.Sprintf("step:%s:%s", ev.App, ev.Step))
	case deploy.EventDone:
		r.log = append(r.log, fmt.Sprintf("done:%s:%s", ev.App, ev.Result.Outcome))
	}
}

func engineWithEvents(rt runtime.Runtime, r *recorded) *deploy.Engine {
	return deploy.New(rt, deploy.Options{
		Settle: 30 * time.Millisecond, PollInterval: 5 * time.Millisecond, StopTimeout: time.Second, OnEvent: r.on,
	})
}

func TestEventsForCreate(t *testing.T) {
	rec := &recorded{}
	mustApply(t, engineWithEvents(newSpy(), rec), spec("web", "nginx:1"))
	want := []string{
		"start:web:create",
		"step:web:pull image",
		"step:web:create container",
		"step:web:start container",
		"step:web:verify container stays running",
		"done:web:applied",
	}
	if !reflect.DeepEqual(rec.log, want) {
		t.Errorf("events\n got: %q\nwant: %q", rec.log, want)
	}
}

func TestEventsForReplace(t *testing.T) {
	rt := newSpy()
	mustApply(t, newEngine(rt), spec("web", "nginx:1"))
	rec := &recorded{}
	mustApply(t, engineWithEvents(rt, rec), spec("web", "nginx:2"))
	want := []string{
		"start:web:replace",
		"step:web:pull image",
		"step:web:prepare staging container",
		"step:web:create new version",
		"step:web:stop old version",
		"step:web:move old version aside",
		"step:web:move new version into place",
		"step:web:start new version",
		"step:web:verify new version stays running",
		"step:web:remove previous version",
		"done:web:applied",
	}
	if !reflect.DeepEqual(rec.log, want) {
		t.Errorf("events\n got: %q\nwant: %q", rec.log, want)
	}
}

func TestEventsShowTheRollback(t *testing.T) {
	rt := newSpy()
	mustApply(t, newEngine(rt), spec("web", "nginx:1"))
	rt.fail["Start:web"] = errors.New("boom")
	rec := &recorded{}
	mustApply(t, engineWithEvents(rt, rec), spec("web", "nginx:2"))

	tail := rec.log[len(rec.log)-3:]
	want := []string{"step:web:start new version", "step:web:roll back to the previous version", "done:web:failed"}
	if !reflect.DeepEqual(tail, want) {
		t.Errorf("tail = %q, want %q", tail, want)
	}
	last := rec.evs[len(rec.evs)-1]
	if last.Result == nil || !last.Result.RolledBack || last.Result.Err == nil {
		t.Errorf("the done event must carry the full result: %+v", last.Result)
	}
}

func TestEventsForFailedFirstDeployCleanup(t *testing.T) {
	rt := newSpy()
	rt.fail["Start:web"] = errors.New("boom")
	rec := &recorded{}
	mustApply(t, engineWithEvents(rt, rec), spec("web", "nginx:1"))
	if got := rec.log[len(rec.log)-2:]; !reflect.DeepEqual(got, []string{"step:web:remove the failed container", "done:web:failed"}) {
		t.Errorf("tail = %q", got)
	}
}

func TestEventsForNoopAndSkipped(t *testing.T) {
	rt := newSpy()
	mustApply(t, newEngine(rt), spec("web", "nginx:1"))

	rec := &recorded{}
	mustApply(t, engineWithEvents(rt, rec), spec("web", "nginx:1"))
	if want := []string{"done:web:unchanged"}; !reflect.DeepEqual(rec.log, want) {
		t.Errorf("noop events = %q, want only %q (nothing started)", rec.log, want)
	}

	rec = &recorded{}
	e := engineWithEvents(newSpy(), rec)
	plan, _ := e.Plan(t.Context(), []domain.ContainerSpec{spec("a", "x:1")})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := e.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if want := []string{"done:a:skipped"}; !reflect.DeepEqual(rec.log, want) {
		t.Errorf("skipped events = %q, want %q", rec.log, want)
	}
}

func TestEventsAreDeliveredInPlanOrderAcrossApps(t *testing.T) {
	rec := &recorded{}
	mustApply(t, engineWithEvents(newSpy(), rec), spec("a", "x:1"), spec("b", "x:1"))
	var order []string
	for _, l := range rec.log {
		if len(l) > 4 && l[:5] == "done:" {
			order = append(order, l)
		}
	}
	if want := []string{"done:a:applied", "done:b:applied"}; !reflect.DeepEqual(order, want) {
		t.Errorf("order = %q", order)
	}
}
