package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/TillmanBuildsTech/barge/internal/domain"
)

// Outcome is the result of applying one item.
type Outcome string

// Outcomes.
const (
	OutcomeApplied   Outcome = "applied"
	OutcomeUnchanged Outcome = "unchanged"
	OutcomeFailed    Outcome = "failed"
	OutcomeSkipped   Outcome = "skipped" // not attempted because the run was cancelled
)

// Result reports what happened for one app.
type Result struct {
	App     string
	Action  Action
	Outcome Outcome
	// Err is why the app failed (or was skipped).
	Err error
	// RolledBack is true when a failure was followed by a rollback that
	// restored the previous state.
	RolledBack bool
	// RollbackErr is set when restoring the previous state itself failed. The
	// host may then need manual attention; the message says what to do.
	RollbackErr error
	// Warnings are non-fatal problems, such as failing to delete the old version.
	Warnings []string
}

// AnyFailed reports whether any result is not a success.
func AnyFailed(rs []Result) bool {
	for _, r := range rs {
		if r.Outcome == OutcomeFailed || r.Outcome == OutcomeSkipped {
			return true
		}
	}
	return false
}

// Apply executes a plan, one app at a time and in plan order. A failing app is
// rolled back and reported; the remaining apps are still attempted unless ctx
// is cancelled, in which case they are reported as skipped.
//
// A plan with blocked items is refused outright (domain.ErrConflict) before
// anything is changed.
//
// Plan and Apply are not atomic: the host is assumed not to change in between.
func (e *Engine) Apply(ctx context.Context, plan Plan) ([]Result, error) {
	if plan.HasBlocked() {
		var why []string
		for _, it := range plan.Items {
			if it.Action == ActionBlocked {
				why = append(why, fmt.Sprintf("%s: %s", it.Spec.Name, it.Reason))
			}
		}
		return nil, fmt.Errorf("%w: refusing to apply, nothing was changed: %s", domain.ErrConflict, strings.Join(why, "; "))
	}
	results := make([]Result, 0, len(plan.Items))
	for _, it := range plan.Items {
		r := Result{App: it.Spec.Name, Action: it.Action}
		if err := ctx.Err(); err != nil {
			r.Outcome, r.Err = OutcomeSkipped, err
			results = append(results, r)
			e.done(r)
			continue
		}
		if it.Action != ActionNoop {
			e.emit(Event{Kind: EventStart, App: r.App, Action: it.Action})
		}
		switch it.Action {
		case ActionNoop:
			r.Outcome = OutcomeUnchanged
		case ActionCreate:
			e.create(ctx, it, &r)
		case ActionStart:
			e.start(ctx, it, &r)
		case ActionReplace:
			e.replace(ctx, it, &r)
		case ActionCleanup:
			e.cleanup(ctx, it, &r)
		case ActionBlocked: // unreachable: refused above
			r.Outcome, r.Err = OutcomeFailed, it.Err
		}
		results = append(results, r)
		e.done(r)
	}
	return results, nil
}

// labelled returns the spec as it will be created: the user's labels plus
// barge's ownership labels. The hash covers the user's spec only.
func labelled(s domain.ContainerSpec, name string) domain.ContainerSpec {
	out := s
	out.Name = name
	out.Labels = make(map[string]string, len(s.Labels)+3)
	for k, v := range s.Labels {
		out.Labels[k] = v
	}
	out.Labels[domain.LabelManagedBy] = domain.ManagedByValue
	out.Labels[domain.LabelApp] = s.Name
	out.Labels[domain.LabelSpecHash] = s.Hash()
	return out
}

func fail(r *Result, step string, err error) {
	r.Outcome = OutcomeFailed
	r.Err = fmt.Errorf("%s %q: %s: %w", r.Action, r.App, step, err)
}

func (e *Engine) create(ctx context.Context, it Item, r *Result) {
	s := it.Spec
	e.step(s.Name, "pull image")
	if err := e.rt.Pull(ctx, s.Image); err != nil {
		fail(r, "pull image", err)
		return
	}
	e.step(s.Name, "create container")
	if _, err := e.rt.Create(ctx, labelled(s, s.Name)); err != nil {
		fail(r, "create container", err)
		return
	}
	e.step(s.Name, "start container")
	if err := e.rt.Start(ctx, s.Name); err != nil {
		fail(r, "start container", err)
		e.discard(ctx, s.Name, r)
		return
	}
	e.step(s.Name, "verify container stays running")
	if err := e.settle(ctx, s.Name); err != nil {
		fail(r, "verify container stays running", err)
		e.discard(ctx, s.Name, r)
		return
	}
	r.Outcome = OutcomeApplied
}

// discard removes a container that was created by a failed first deployment,
// so a broken half-deployment is never left behind.
func (e *Engine) discard(ctx context.Context, name string, r *Result) {
	e.step(name, "remove the failed container")
	cctx, cancel := detached(ctx)
	defer cancel()
	if err := e.rt.Remove(cctx, name, true); err != nil {
		r.RollbackErr = fmt.Errorf("could not remove the failed container %q (remove it manually): %w", name, err)
		return
	}
	r.RolledBack = true
}

func (e *Engine) start(ctx context.Context, it Item, r *Result) {
	name := it.Spec.Name
	e.step(name, "start container")
	if err := e.rt.Start(ctx, name); err != nil {
		fail(r, "start container", err)
		return
	}
	e.step(name, "verify container stays running")
	if err := e.settle(ctx, name); err != nil {
		fail(r, "verify container stays running", err)
		return
	}
	r.Outcome = OutcomeApplied
}

// replace swaps in a new version while keeping the old one restorable:
//
//	pull -> create <name>-barge-new -> stop old -> rename old to <name>-barge-old
//	     -> rename new to <name> -> start -> settle -> remove old
//
// Creating the new container before touching the old one means a bad spec
// fails with zero downtime. Once the old one is stopped, any failure triggers
// rollback.
func (e *Engine) replace(ctx context.Context, it Item, r *Result) {
	s := it.Spec
	name, newName, oldName := s.Name, s.Name+tempNewSuffix, s.Name+tempOldSuffix
	oldWasRunning := it.Current != nil && it.Current.Status == domain.StatusRunning

	e.step(name, "pull image")
	if err := e.rt.Pull(ctx, s.Image); err != nil {
		fail(r, "pull image", err)
		return
	}
	e.step(name, "prepare staging container")
	for _, tmp := range []string{newName, oldName} {
		if err := e.clearStaleTemp(ctx, s.Name, tmp); err != nil {
			fail(r, "prepare staging container", err)
			return
		}
	}
	e.step(name, "create new version")
	if _, err := e.rt.Create(ctx, labelled(s, newName)); err != nil {
		fail(r, "create new version", err)
		return
	}

	var st rollbackState
	st.oldWasRunning = oldWasRunning
	steps := []struct {
		what string
		do   func() error
	}{
		{"stop old version", func() error {
			if !oldWasRunning {
				return nil
			}
			st.stopAttempted = true
			return e.rt.Stop(ctx, name, e.opt.StopTimeout)
		}},
		{"move old version aside", func() error {
			if err := e.rt.Rename(ctx, name, oldName); err != nil {
				return err
			}
			st.renamedOld = true
			return nil
		}},
		{"move new version into place", func() error {
			if err := e.rt.Rename(ctx, newName, name); err != nil {
				return err
			}
			st.renamedNew = true
			return nil
		}},
		{"start new version", func() error { return e.rt.Start(ctx, name) }},
		{"verify new version stays running", func() error { return e.settle(ctx, name) }},
	}
	for _, step := range steps {
		e.step(name, step.what)
		if err := step.do(); err != nil {
			fail(r, step.what, err)
			e.rollback(ctx, s, st, r)
			return
		}
	}

	r.Outcome = OutcomeApplied
	e.step(name, "remove previous version")
	cctx, cancel := detached(ctx)
	defer cancel()
	if err := e.rt.Remove(cctx, oldName, true); err != nil {
		r.Warnings = append(r.Warnings, fmt.Sprintf("deployed, but could not remove the previous version %q (remove it manually): %v", oldName, err))
	}
}

// cleanup removes a leftover previous version next to a healthy, current
// container. The running container is never touched.
func (e *Engine) cleanup(ctx context.Context, it Item, r *Result) {
	name := it.Spec.Name
	oldName := name + tempOldSuffix
	e.step(name, "remove leftover previous version")
	cctx, cancel := detached(ctx)
	defer cancel()
	if err := e.rt.Remove(cctx, oldName, true); err != nil {
		fail(r, "remove leftover previous version "+oldName, err)
		return
	}
	r.Outcome = OutcomeApplied
}

// clearStaleTemp removes a leftover staging container from an earlier crashed
// run, but only if barge owns it; anything else is a conflict.
func (e *Engine) clearStaleTemp(ctx context.Context, app, tmp string) error {
	st, err := e.rt.Inspect(ctx, tmp)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !owned(st, app) {
		return fmt.Errorf("%w: container %q exists and is not managed by barge", domain.ErrConflict, tmp)
	}
	return e.rt.Remove(ctx, tmp, true)
}

type rollbackState struct {
	oldWasRunning bool
	stopAttempted bool
	renamedOld    bool
	renamedNew    bool
}

// rollback restores the previous version. It runs on a context detached from
// the caller's, so Ctrl-C mid-deploy cannot strand the host half-switched.
func (e *Engine) rollback(ctx context.Context, s domain.ContainerSpec, st rollbackState, r *Result) {
	e.step(s.Name, "roll back to the previous version")
	cctx, cancel := detached(ctx)
	defer cancel()
	name, newName, oldName := s.Name, s.Name+tempNewSuffix, s.Name+tempOldSuffix

	var errs []error
	newCurrent := newName
	if st.renamedNew {
		newCurrent = name
	}
	if err := e.rt.Remove(cctx, newCurrent, true); err != nil {
		errs = append(errs, fmt.Errorf("remove new version %q: %w", newCurrent, err))
	}
	restored := true
	if st.renamedOld {
		if err := e.rt.Rename(cctx, oldName, name); err != nil {
			errs = append(errs, fmt.Errorf("rename previous version %q back to %q: %w", oldName, name, err))
			restored = false
		}
	}
	if restored && st.oldWasRunning && st.stopAttempted {
		if err := e.rt.Start(cctx, name); err != nil {
			errs = append(errs, fmt.Errorf("restart previous version %q: %w", name, err))
		}
	}
	if len(errs) > 0 {
		r.RollbackErr = fmt.Errorf("rollback incomplete, manual recovery needed (previous version may be %q or %q): %w",
			oldName, name, errors.Join(errs...))
		return
	}
	r.RolledBack = true
}

// settle verifies the container is running now and stays running for
// Options.Settle, catching crash loops and instant exits that a successful
// `start` hides.
func (e *Engine) settle(ctx context.Context, name string) error {
	deadline := time.Now().Add(e.opt.Settle)
	for {
		st, err := e.rt.Inspect(ctx, name)
		if err != nil {
			return err
		}
		if st.Status != domain.StatusRunning {
			return fmt.Errorf("container is %s, expected it to keep running", st.Status)
		}
		if !time.Now().Before(deadline) {
			return nil
		}
		t := time.NewTimer(e.opt.PollInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("waiting for %q to settle: %w", name, ctx.Err())
		case <-t.C:
		}
	}
}
