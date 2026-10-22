// Package tui is barge's interactive front end. It is a thin client of
// session.Session, the same engine the plan and apply commands use, so it has
// exactly the same safety properties: secrets are resolved first, everything is
// planned before anything changes, a problem anywhere blocks the apply, and a
// failed or cancelled rollout is rolled back.
//
// The model never reads session state while an apply is running (the engine
// mutates it from another goroutine). It builds its own immutable view-model
// from messages instead.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/TillmanBuildsTech/barge/internal/deploy"
	"github.com/TillmanBuildsTech/barge/internal/secrets"
	"github.com/TillmanBuildsTech/barge/internal/session"
)

// Config configures a Model.
type Config struct {
	Version    string
	ConfigPath string
	Session    *session.Session
	// NoAnimation freezes the spinner, which makes rendering deterministic.
	NoAnimation bool
	// Context bounds all operations; cancelling it behaves like Ctrl-C.
	// Defaults to context.Background().
	Context context.Context
}

type phase int

const (
	phasePlanning phase = iota
	phaseReview
	phaseConfirm
	phaseApplying
	phaseDone
)

type status int

const (
	stPlanned status = iota
	stPending
	stRunning
	stApplied
	stUnchanged
	stFailed
	stSkipped
)

type itemView struct {
	app, action, reason string
	status              status
	step                string
	err                 string
	rolledBack          bool
	rollbackErr         string
	warnings            []string
}

type targetView struct {
	name, driver, typ string
	err               string
	items             []*itemView
}

// ---- messages ----

type planDoneMsg struct {
	targets []*targetView
	changes int
	planOK  bool
	red     *secrets.Redactor
}

type eventMsg struct{ ev session.Event }

type applyFinishedMsg struct {
	err string
	red *secrets.Redactor
}

type tickMsg struct{}

// CancelMsg asks the model to stop what it is doing, exactly like Ctrl-C. Send
// it when the process receives SIGTERM.
type CancelMsg struct{}

// Model is the Bubble Tea model.
type Model struct {
	cfg    Config
	base   context.Context
	cancel context.CancelFunc

	phase   phase
	w, h    int
	offset  int
	frame   int
	targets []*targetView
	index   map[string]*itemView
	changes int
	planOK  bool
	applied bool
	red     *secrets.Redactor

	notice     string
	cancelling bool
	applyErr   string
	q          *queue
}

// New creates the model. Run starts planning as soon as the program starts.
func New(cfg Config) *Model {
	base := cfg.Context
	if base == nil {
		base = context.Background()
	}
	return &Model{cfg: cfg, base: base, index: map[string]*itemView{}, red: secrets.NewRedactor(nil)}
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd { return tea.Batch(m.planCmd(), m.tick()) }

func (m *Model) tick() tea.Cmd {
	if m.cfg.NoAnimation {
		return nil
	}
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *Model) opContext() context.Context {
	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithCancel(m.base)
	m.cancel = cancel
	return ctx
}

// planCmd plans in a command goroutine and returns plain data only.
func (m *Model) planCmd() tea.Cmd {
	ctx := m.opContext()
	sess := m.cfg.Session
	m.phase, m.notice, m.offset = phasePlanning, "", 0
	m.applied, m.cancelling, m.applyErr = false, false, ""
	return func() tea.Msg {
		runs := sess.Plan(ctx)
		return planDoneMsg{targets: buildViews(runs), changes: sess.Changes(), planOK: sess.PlanOK(), red: sess.Redactor()}
	}
}

func buildViews(runs []*session.TargetRun) []*targetView {
	out := make([]*targetView, 0, len(runs))
	for _, r := range runs {
		tv := &targetView{name: r.Target.Name, driver: r.Target.Runtime.Driver, typ: r.Target.Type}
		if r.Err != nil {
			tv.err = r.Err.Error()
		}
		for _, it := range r.Plan.Items {
			iv := &itemView{app: it.Spec.Name, action: string(it.Action), reason: it.Reason}
			if it.Action == deploy.ActionNoop {
				iv.status = stUnchanged
			}
			tv.items = append(tv.items, iv)
		}
		out = append(out, tv)
	}
	return out
}

func (m *Model) applyCmd() tea.Cmd {
	ctx := m.opContext()
	sess := m.cfg.Session
	q := newQueue()
	m.q = q
	m.phase, m.notice, m.offset = phaseApplying, "", 0
	m.applied, m.cancelling = true, false
	for _, t := range m.targets {
		for _, it := range t.items {
			if it.action == string(deploy.ActionNoop) {
				it.status = stUnchanged
			} else {
				it.status = stPending
			}
		}
	}
	run := func() tea.Msg {
		err := sess.Apply(ctx, func(ev session.Event) { q.push(eventMsg{ev}) })
		fin := applyFinishedMsg{red: sess.Redactor()}
		if err != nil {
			fin.err = err.Error()
		}
		q.push(fin)
		return nil
	}
	return tea.Batch(run, waitFor(q))
}

func waitFor(q *queue) tea.Cmd { return func() tea.Msg { return q.pop() } }

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.clamp()
	case tickMsg:
		if m.phase == phasePlanning || m.phase == phaseApplying {
			m.frame++
			return m, m.tick()
		}
	case planDoneMsg:
		m.targets, m.changes, m.planOK, m.red = msg.targets, msg.changes, msg.planOK, msg.red
		m.index = map[string]*itemView{}
		for _, t := range m.targets {
			for _, it := range t.items {
				m.index[t.name+"/"+it.app] = it
			}
		}
		m.phase = phaseReview
		m.clamp()
	case eventMsg:
		m.onEvent(msg.ev)
		return m, waitFor(m.q)
	case applyFinishedMsg:
		m.red = msg.red
		m.applyErr = msg.err
		for _, t := range m.targets {
			for _, it := range t.items {
				if it.status == stPending || it.status == stRunning {
					it.status = stSkipped
				}
			}
		}
		m.phase, m.cancelling = phaseDone, false
		m.clamp()
	case CancelMsg:
		return m.interrupt()
	case tea.KeyPressMsg:
		return m.onKey(msg)
	}
	return m, nil
}

func (m *Model) onEvent(ev session.Event) {
	it := m.index[ev.Target+"/"+ev.App]
	if it == nil {
		return
	}
	switch ev.Kind {
	case deploy.EventStart:
		it.status, it.step = stRunning, "starting"
	case deploy.EventStep:
		it.status, it.step = stRunning, ev.Step
	case deploy.EventDone:
		r := ev.Result
		it.step = ""
		switch r.Outcome {
		case deploy.OutcomeApplied:
			it.status = stApplied
		case deploy.OutcomeUnchanged:
			it.status = stUnchanged
		case deploy.OutcomeSkipped:
			it.status = stSkipped
		case deploy.OutcomeFailed:
			it.status = stFailed
		}
		if r.Err != nil {
			it.err = r.Err.Error()
		}
		it.rolledBack = r.RolledBack
		if r.RollbackErr != nil {
			it.rollbackErr = r.RollbackErr.Error()
		}
		it.warnings = append([]string(nil), r.Warnings...)
	}
}

// interrupt is Ctrl-C / SIGTERM. While applying it cancels the work (the
// engine rolls back the app in flight and skips the rest) and keeps the UI
// open to show the outcome; anywhere else it quits.
func (m *Model) interrupt() (tea.Model, tea.Cmd) {
	if m.phase == phaseApplying {
		if !m.cancelling {
			m.cancelling = true
			if m.cancel != nil {
				m.cancel()
			}
		}
		return m, nil
	}
	return m, m.quit()
}

func (m *Model) quit() tea.Cmd {
	if m.cancel != nil {
		m.cancel()
	}
	return tea.Quit
}

func (m *Model) onKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if s == "ctrl+c" {
		return m.interrupt()
	}
	switch s {
	case "up", "k":
		m.scroll(-1)
		return m, nil
	case "down", "j":
		m.scroll(1)
		return m, nil
	case "pgup":
		m.scroll(-m.page())
		return m, nil
	case "pgdown":
		m.scroll(m.page())
		return m, nil
	case "home", "g":
		m.offset = 0
		return m, nil
	case "end", "G":
		m.offset = 1 << 30
		m.clamp()
		return m, nil
	}

	switch m.phase {
	case phasePlanning:
		if s == "q" {
			return m, m.quit()
		}
	case phaseReview:
		switch s {
		case "q":
			return m, m.quit()
		case "r":
			return m, tea.Batch(m.planCmd(), m.tick())
		case "a", "enter":
			switch {
			case !m.planOK:
				m.notice = "cannot apply: fix the problems above, then press r to re-plan"
			case m.changes == 0:
				m.notice = "nothing to apply: everything is up to date"
			default:
				m.phase, m.notice = phaseConfirm, ""
			}
		}
	case phaseConfirm:
		switch s {
		case "y":
			return m, tea.Batch(m.applyCmd(), m.tick())
		case "n", "esc", "q":
			m.phase = phaseReview
		}
	case phaseApplying:
		if s == "q" || s == "esc" {
			return m.interrupt()
		}
	case phaseDone:
		switch s {
		case "q":
			return m, m.quit()
		case "r":
			return m, tea.Batch(m.planCmd(), m.tick())
		}
	}
	return m, nil
}

// ExitCode is the process exit code that matches what the user saw: 0 when
// the plan is clean and any apply fully succeeded, 1 otherwise.
func (m *Model) ExitCode() int {
	if m.applied {
		if m.applyErr != "" {
			return 1
		}
		for _, t := range m.targets {
			if t.err != "" {
				return 1
			}
			for _, it := range t.items {
				if it.status == stFailed || it.status == stSkipped || it.status == stPending || it.status == stRunning {
					return 1
				}
			}
		}
		return 0
	}
	if m.phase == phasePlanning || !m.planOK {
		return 1
	}
	return 0
}

// counts tallies the results of an apply.
func (m *Model) counts() (applied, unchanged, failed, skipped int) {
	for _, t := range m.targets {
		for _, it := range t.items {
			switch it.status {
			case stApplied:
				applied++
			case stUnchanged:
				unchanged++
			case stFailed:
				failed++
			case stSkipped, stPending, stRunning:
				skipped++
			case stPlanned:
			}
		}
	}
	return
}

// Summary is a plain-text, secret-free recap for the terminal scrollback,
// printed after the alternate screen closes.
func (m *Model) Summary() string {
	if !m.applied {
		if m.phase != phasePlanning && !m.planOK {
			return "barge: the plan has problems; no changes were applied"
		}
		return "barge: no changes were applied"
	}
	a, u, f, s := m.counts()
	var parts []string
	for _, p := range []struct {
		n    int
		what string
	}{{a, "applied"}, {u, "unchanged"}, {f, "failed"}, {s, "skipped"}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.what))
		}
	}
	if len(parts) == 0 {
		parts = []string{"nothing changed"}
	}
	var b strings.Builder
	b.WriteString("barge: " + strings.Join(parts, ", "))
	for _, t := range m.targets {
		for _, it := range t.items {
			if it.status == stFailed {
				first, _, _ := strings.Cut(it.err, "\n")
				fmt.Fprintf(&b, "\n  FAILED %s/%s: %s", t.name, it.app, m.red.Redact(first))
				if it.rollbackErr != "" {
					b.WriteString(" (ROLLBACK INCOMPLETE: manual recovery needed)")
				}
			}
		}
	}
	if m.applyErr != "" {
		b.WriteString("\n  " + m.red.Redact(m.applyErr))
	}
	return b.String()
}
