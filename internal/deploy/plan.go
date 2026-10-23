package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/TillmanBuildsTech/barge/internal/domain"
)

// Action is what the engine intends to do for one app.
type Action string

// Actions.
const (
	ActionNoop    Action = "noop"    // already running the desired spec
	ActionCreate  Action = "create"  // no container yet
	ActionStart   Action = "start"   // right spec, but not running
	ActionReplace Action = "replace" // spec changed: staged swap with rollback
	ActionCleanup Action = "cleanup" // up to date, but remove a leftover previous version
	ActionBlocked Action = "blocked" // cannot proceed safely; needs a human
)

// Item is the plan for one app.
type Item struct {
	Spec    domain.ContainerSpec
	Action  Action
	Reason  string                 // human-readable; never contains env values
	Current *domain.ContainerState // the existing container, if any
	Err     error                  // set when Action is ActionBlocked
}

// Plan is an ordered list of per-app items.
type Plan struct {
	Items []Item
}

// HasChanges reports whether applying would change anything.
func (p Plan) HasChanges() bool {
	for _, it := range p.Items {
		if it.Action != ActionNoop {
			return true
		}
	}
	return false
}

// HasBlocked reports whether any app cannot be deployed safely.
func (p Plan) HasBlocked() bool {
	for _, it := range p.Items {
		if it.Action == ActionBlocked {
			return true
		}
	}
	return false
}

// String renders the plan for humans.
func (p Plan) String() string {
	var b strings.Builder
	for _, it := range p.Items {
		sym, label := "?", string(it.Action)
		switch it.Action {
		case ActionCreate:
			sym = "+"
		case ActionReplace:
			sym = "~"
		case ActionStart:
			sym = ">"
		case ActionCleanup:
			sym = "-"
		case ActionNoop:
			sym, label = "=", "unchanged"
		case ActionBlocked:
			sym = "!"
		}
		fmt.Fprintf(&b, "  %s %-16s %-10s %s\n", sym, it.Spec.Name, label, it.Reason)
	}
	return b.String()
}

// owned reports whether st is a container barge created for the named app.
func owned(st domain.ContainerState, app string) bool {
	return st.Labels[domain.LabelManagedBy] == domain.ManagedByValue && st.Labels[domain.LabelApp] == app
}

// validateSpecs checks every spec and returns all problems at once.
func validateSpecs(specs []domain.ContainerSpec) error {
	var errs []error
	seen := map[string]bool{}
	for _, s := range specs {
		if err := s.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("app %q: %w", s.Name, err))
		}
		for k := range s.Labels {
			if domain.ReservedLabel(k) {
				errs = append(errs, fmt.Errorf("%w: app %q: label %q is reserved for barge", domain.ErrInvalid, s.Name, k))
			}
		}
		for _, suf := range []string{tempNewSuffix, tempOldSuffix} {
			if strings.HasSuffix(s.Name, suf) {
				errs = append(errs, fmt.Errorf("%w: app %q: names ending in %q are reserved for barge's temporary containers", domain.ErrInvalid, s.Name, suf))
			}
		}
		if seen[s.Name] {
			errs = append(errs, fmt.Errorf("%w: duplicate app name %q", domain.ErrInvalid, s.Name))
		}
		seen[s.Name] = true
	}
	return errors.Join(errs...)
}

// Plan inspects the host and decides what to do for each spec. It performs no
// mutation. Runtime failures abort the whole plan (we must not guess); apps
// that cannot be deployed safely come back as ActionBlocked items.
func (e *Engine) Plan(ctx context.Context, specs []domain.ContainerSpec) (Plan, error) {
	if err := validateSpecs(specs); err != nil {
		return Plan{}, err
	}
	plan := Plan{Items: make([]Item, 0, len(specs))}
	for _, s := range specs {
		it, err := e.plan(ctx, s)
		if err != nil {
			return Plan{}, fmt.Errorf("plan %q: %w", s.Name, err)
		}
		plan.Items = append(plan.Items, it)
	}
	return plan, nil
}

func (e *Engine) plan(ctx context.Context, s domain.ContainerSpec) (Item, error) {
	cur, err := e.rt.Inspect(ctx, s.Name)
	exists := true
	switch {
	case errors.Is(err, domain.ErrNotFound):
		exists = false
	case err != nil:
		return Item{}, err
	}

	// A "<name>-barge-old" is either harmless debris or the only good copy of
	// the app. It is harmless only when the live container is barge's own and
	// running (so a finished replace failed to delete its backup) and the
	// backup is barge's too. Anything else, such as a deploy that died between
	// its rename steps, needs a human: barge refuses to guess which is good.
	oldName := s.Name + tempOldSuffix
	staleOld := false
	switch old, err := e.rt.Inspect(ctx, oldName); {
	case err == nil:
		if !exists || !owned(cur, s.Name) || cur.Status != domain.StatusRunning || !owned(old, s.Name) {
			return blocked(s, fmt.Sprintf("a previous deployment was interrupted: container %q still exists; "+
				"inspect it, then rename it back to %q or remove it, and re-run", oldName, s.Name)), nil
		}
		staleOld = true
	case !errors.Is(err, domain.ErrNotFound):
		return Item{}, err
	}

	if !exists {
		return Item{Spec: s, Action: ActionCreate, Reason: fmt.Sprintf("no container named %q", s.Name)}, nil
	}
	if !owned(cur, s.Name) {
		return blocked(s, fmt.Sprintf("container %q exists and is not managed by barge", s.Name)), nil
	}
	item := Item{Spec: s, Current: &cur}
	switch {
	case cur.Labels[domain.LabelSpecHash] != s.Hash():
		item.Action = ActionReplace
		if cur.Image != s.Image {
			item.Reason = fmt.Sprintf("image changed (%s -> %s)", cur.Image, s.Image)
		} else {
			item.Reason = "configuration changed"
		}
		if staleOld {
			item.Reason += fmt.Sprintf("; also removes leftover %q", oldName)
		}
	case cur.Status != domain.StatusRunning:
		item.Action, item.Reason = ActionStart, fmt.Sprintf("container is %s", cur.Status)
	default:
		item.Action, item.Reason = ActionNoop, "up to date"
		if staleOld {
			item.Action, item.Reason = ActionCleanup, fmt.Sprintf("up to date; remove leftover previous version %q", oldName)
		}
	}
	return item, nil
}

func blocked(s domain.ContainerSpec, reason string) Item {
	return Item{Spec: s, Action: ActionBlocked, Reason: reason, Err: fmt.Errorf("%w: %s", domain.ErrConflict, reason)}
}
