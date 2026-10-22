package deploy

// EventKind distinguishes progress events.
type EventKind string

// Event kinds.
const (
	// EventStart: work on an app began (not sent for unchanged apps).
	EventStart EventKind = "start"
	// EventStep: the app moved on to a new named step.
	EventStep EventKind = "step"
	// EventDone: the app finished; Result is set.
	EventDone EventKind = "done"
)

// Event reports progress while applying. It exists so a UI can show what is
// happening live; it carries no secret values (step names are fixed strings).
type Event struct {
	Kind   EventKind
	App    string
	Action Action  // set on EventStart
	Step   string  // set on EventStep
	Result *Result // set on EventDone
}

func (e *Engine) emit(ev Event) {
	if e.opt.OnEvent != nil {
		e.opt.OnEvent(ev)
	}
}

func (e *Engine) step(app, name string) { e.emit(Event{Kind: EventStep, App: app, Step: name}) }

func (e *Engine) done(r Result) {
	e.emit(Event{Kind: EventDone, App: r.App, Result: &r})
}
