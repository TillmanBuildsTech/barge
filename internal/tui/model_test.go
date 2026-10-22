package tui_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/TillmanBuildsTech/barge/internal/domain"
	"github.com/TillmanBuildsTech/barge/internal/tui"
)

func TestStartsPlanningThenShowsReview(t *testing.T) {
	fx := newFixture(t, baseCfg)
	fx.model.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	fx.contains("Planning")
	fx.lacks("apply")

	fx.drive(fx.model.Init())
	fx.contains("barge", "v9.9.9", "barge.yaml", "dev", "fake", "web", "api", "create", "2 changes", "apply")
	if got := len(fx.containers()); got != 0 {
		t.Errorf("reviewing a plan must not change anything, found %d containers", got)
	}
	if !fx.model.View().AltScreen {
		t.Error("the TUI should use the alternate screen")
	}
}

func TestApplyFlowWithConfirmation(t *testing.T) {
	fx := newFixture(t, baseCfg).start()

	fx.press("a")
	fx.contains("Apply 2 changes", "y/n")
	if len(fx.containers()) != 0 {
		t.Fatal("nothing may happen before confirmation")
	}

	fx.press("n")
	fx.contains("2 changes")
	fx.lacks("y/n")

	fx.press("a")
	fx.press("y")
	fx.contains("applied", "web", "api", "2 applied")
	fx.lacks("y/n")
	if got := names(fx.containers()); got != "web=running,api=running" && got != "api=running,web=running" {
		t.Errorf("containers = %s", got)
	}
	if fx.model.ExitCode() != 0 {
		t.Errorf("exit code = %d", fx.model.ExitCode())
	}
}

func TestOnlyExplicitYesConfirms(t *testing.T) {
	fx := newFixture(t, baseCfg).start()
	fx.press("a")
	for _, k := range []string{"enter", "x", "esc"} {
		fx.press(k)
		if len(fx.containers()) != 0 {
			t.Fatalf("key %q must not confirm an apply", k)
		}
		if k != "esc" {
			fx.press("a") // re-open the prompt (esc already closed it)
		}
	}
}

func TestEnterOpensTheSamePromptAsA(t *testing.T) {
	fx := newFixture(t, baseCfg).start()
	fx.press("enter")
	fx.contains("Apply 2 changes")
}

func TestNothingToApply(t *testing.T) {
	fx := newFixture(t, baseCfg).start()
	fx.press("a")
	fx.press("y")
	fx.press("r") // re-plan after a successful apply
	fx.contains("up to date")
	fx.press("a")
	fx.contains("nothing to apply")
	fx.lacks("y/n")
}

func TestLiveProgressIsShownStepByStep(t *testing.T) {
	fx := newFixture(t, baseCfg).start()
	fx.press("a")
	_, cmd := fx.model.Update(key("y"))

	batch := cmd().(tea.BatchMsg) // [run apply, wait for first event]
	batch[0]()                    // the apply goroutine finishes, queueing all events
	fx.contains("Applying")

	// Feed events one at a time and watch the screen follow along.
	next := batch[1]
	var sawStep, sawRunning bool
	for i := 0; i < 400; i++ {
		msg := next()
		_, c := fx.model.Update(msg)
		s := fx.screen()
		sawStep = sawStep || strings.Contains(s, "pull image")
		sawRunning = sawRunning || strings.Contains(s, "web") && strings.Contains(s, "create container")
		if c == nil {
			break
		}
		next = c
	}
	if !sawStep || !sawRunning {
		t.Errorf("progress was not visible: step=%v running=%v", sawStep, sawRunning)
	}
	fx.contains("2 applied")
}

func TestPlanProblemsBlockApplyAndSayWhy(t *testing.T) {
	fx := newFixture(t, baseCfg)
	// A container named "web" that barge did not create.
	if _, err := fx.fake.Create(t.Context(), domain.ContainerSpec{Name: "web", Image: "theirs:1"}); err != nil {
		t.Fatal(err)
	}
	fx.start()
	fx.contains("blocked", "not managed by barge", "1 problem")

	fx.press("a")
	fx.contains("cannot apply")
	fx.lacks("y/n")
	fx.press("y")
	if got := len(fx.containers()); got != 1 {
		t.Errorf("a blocked plan must change nothing, containers=%d", got)
	}
	if fx.model.ExitCode() != 1 {
		t.Errorf("a plan with problems exits 1, got %d", fx.model.ExitCode())
	}
}

func TestUnresolvableSecretIsShownAsTargetError(t *testing.T) {
	os.Unsetenv("BARGE_TUI_MISSING") //nolint:errcheck
	y := strings.Replace(baseCfg, `{name: web, image: "nginx:1"}`, `{name: web, image: "nginx:1", secrets: {DB_PASSWORD: {env: BARGE_TUI_MISSING}}}`, 1)
	fx := newFixture(t, y).start()
	fx.contains("DB_PASSWORD", "BARGE_TUI_MISSING", "problem")
	fx.press("a")
	fx.contains("cannot apply")
	if len(fx.containers()) != 0 {
		t.Error("nothing may be deployed")
	}
}

func TestFailureAndRollbackAreExplained(t *testing.T) {
	fx := newFixture(t, baseCfg)
	// A running, barge-owned "web" built from an older spec, so apply must replace it.
	old := domain.ContainerSpec{Name: "web", Image: "nginx:0", Labels: map[string]string{
		domain.LabelManagedBy: domain.ManagedByValue, domain.LabelApp: "web", domain.LabelSpecHash: "stale",
	}}
	if _, err := fx.fake.Create(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	if err := fx.fake.Start(t.Context(), "web"); err != nil {
		t.Fatal(err)
	}
	fx.flaky.startFails = 1 // the new version fails to start
	fx.start()
	fx.contains("replace")

	fx.press("a")
	fx.press("y")
	fx.contains("FAILED", "injected start failure", "rolled back")
	if fx.model.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1", fx.model.ExitCode())
	}
	if !strings.Contains(names(fx.containers()), "web=running") {
		t.Errorf("the old version must be running again: %s", names(fx.containers()))
	}
}

func TestCancelDuringApplySkipsTheRest(t *testing.T) {
	fx := newFixture(t, baseCfg).start()
	fx.press("a")
	_, cmd := fx.model.Update(key("y"))
	// Ctrl-C arrives before the work has started.
	_, c2 := fx.model.Update(key("ctrl+c"))
	fx.contains("Cancelling")
	fx.drive(cmd)
	fx.drive(c2)
	fx.contains("skipped")
	if len(fx.containers()) != 0 {
		t.Errorf("cancelled before any work: %s", names(fx.containers()))
	}
	if fx.model.ExitCode() != 1 {
		t.Errorf("a cancelled apply exits 1, got %d", fx.model.ExitCode())
	}
}

func TestQuitKeys(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		fx := newFixture(t, baseCfg).start()
		if !fx.press(k) {
			t.Errorf("%q on the review screen should quit", k)
		}
	}
	fx := newFixture(t, baseCfg).start()
	fx.press("a")
	if fx.press("q") {
		t.Error("q at the prompt must not quit, it should close the prompt")
	}
	fx.lacks("y/n")
}

func TestQuitAfterDone(t *testing.T) {
	fx := newFixture(t, baseCfg).start()
	fx.press("a")
	fx.press("y")
	if !fx.press("q") {
		t.Error("q after the run should quit")
	}
}

func TestExternalCancelSignalQuitsWhenIdleAndCancelsWhenApplying(t *testing.T) {
	fx := newFixture(t, baseCfg).start()
	_, cmd := fx.model.Update(tui.CancelMsg{})
	if !fx.drive(cmd) {
		t.Error("SIGTERM on an idle screen should quit")
	}

	fx = newFixture(t, baseCfg).start()
	fx.press("a")
	_, run := fx.model.Update(key("y"))
	fx.model.Update(tui.CancelMsg{})
	fx.contains("Cancelling")
	fx.drive(run)
	fx.contains("skipped")
}

func TestSummaryForTheTerminalScrollback(t *testing.T) {
	fx := newFixture(t, baseCfg).start()
	if s := fx.model.Summary(); !strings.Contains(s, "no changes were applied") {
		t.Errorf("summary before applying = %q", s)
	}
	fx.press("a")
	fx.press("y")
	if s := fx.model.Summary(); !strings.Contains(s, "2 applied") || strings.Contains(s, "\x1b") {
		t.Errorf("summary after applying = %q (must be plain text)", s)
	}
}

func TestReplanIsAvailableAfterAFailure(t *testing.T) {
	fx := newFixture(t, baseCfg)
	fx.flaky.pullErr = errors.New("registry down")
	fx.start()
	fx.press("a")
	fx.press("y")
	fx.contains("FAILED", "registry down")
	fx.flaky.pullErr = nil
	fx.press("r")
	fx.contains("create")
	fx.press("a")
	fx.press("y")
	fx.contains("2 applied")
	if fx.model.ExitCode() != 0 {
		t.Errorf("after recovering, exit code = %d", fx.model.ExitCode())
	}
}
