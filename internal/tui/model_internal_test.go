package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The UI never lets the session refuse an apply, but if it ever did, the user
// must see a failure and nothing may be left looking "in progress".
func TestRefusedApplyIsAFailureNotASuccess(t *testing.T) {
	m := New(Config{NoAnimation: true})
	m.phase, m.applied, m.planOK = phaseApplying, true, true
	m.targets = []*targetView{{name: "dev", driver: "fake", typ: "local", items: []*itemView{
		{app: "web", action: "create", status: stPending},
		{app: "api", action: "create", status: stRunning},
	}}}

	m.Update(applyFinishedMsg{err: "refusing to apply: boom", red: m.red})

	if m.phase != phaseDone || m.ExitCode() != 1 {
		t.Fatalf("phase=%v exit=%d", m.phase, m.ExitCode())
	}
	screen := ansi.Strip(m.View().Content)
	if !strings.Contains(screen, "refusing to apply: boom") || strings.Contains(screen, "waiting") {
		t.Errorf("screen:\n%s", screen)
	}
	if s := m.Summary(); !strings.Contains(s, "boom") || !strings.Contains(s, "skipped") {
		t.Errorf("summary = %q", s)
	}
}

func TestApplyErrorAloneForcesFailureExitCode(t *testing.T) {
	m := New(Config{NoAnimation: true})
	m.phase, m.applied, m.planOK = phaseApplying, true, true
	m.targets = []*targetView{{name: "dev", items: []*itemView{{app: "web", action: "create", status: stApplied}}}}
	m.Update(applyFinishedMsg{err: "boom", red: m.red})
	if m.ExitCode() != 1 {
		t.Errorf("an apply error must fail the run even if every app looks fine, exit=%d", m.ExitCode())
	}
}
