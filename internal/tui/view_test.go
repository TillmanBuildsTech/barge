package tui_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func manyApps(n int) string {
	var apps, names []string
	for i := 0; i < n; i++ {
		apps = append(apps, fmt.Sprintf("  - {name: app%02d, image: \"img:1\"}", i))
		names = append(names, fmt.Sprintf("app%02d", i))
	}
	return "version: 1\nruntime: {driver: fake}\ndeploy: {settle: 5ms}\napps:\n" + strings.Join(apps, "\n") +
		"\ntargets:\n  - {name: dev, type: local, apps: [" + strings.Join(names, ", ") + "]}\n"
}

func TestNoLineIsWiderThanTheTerminal(t *testing.T) {
	for _, w := range []int{20, 40, 80, 200} {
		fx := newFixture(t, baseCfg)
		fx.flaky.pullErr = errors.New("registry said no, with a very long explanation that goes on and on and on")
		fx.model.Update(tea.WindowSizeMsg{Width: w, Height: 40})
		fx.drive(fx.model.Init())
		fx.press("a")
		fx.press("y")
		for _, line := range strings.Split(fx.screen(), "\n") {
			if got := ansi.StringWidth(line); got > w {
				t.Errorf("width %d: line is %d wide: %q", w, got, line)
			}
		}
	}
}

func TestLongListsScrollWithinTheHeight(t *testing.T) {
	fx := newFixture(t, manyApps(30))
	fx.model.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	fx.drive(fx.model.Init())

	if n := len(strings.Split(fx.screen(), "\n")); n > 12 {
		t.Fatalf("view is %d lines tall in a 12 line terminal", n)
	}
	fx.contains("app00")
	fx.lacks("app29")

	for i := 0; i < 60; i++ {
		fx.press("down")
	}
	fx.contains("app29")
	fx.lacks("app00")
	if n := len(strings.Split(fx.screen(), "\n")); n > 12 {
		t.Errorf("view is %d lines tall after scrolling", n)
	}
	fx.press("pgdown") // scrolling past the end is harmless
	fx.contains("app29")
	for i := 0; i < 100; i++ {
		fx.press("up")
	}
	fx.contains("app00")
}

func TestScrollingNeverChangesWhatWillBeApplied(t *testing.T) {
	fx := newFixture(t, manyApps(5))
	fx.press("down")
	fx.start()
	fx.press("down")
	fx.press("a")
	fx.press("y")
	if n := len(fx.containers()); n != 5 {
		t.Errorf("containers = %d", n)
	}
}

func TestSecretsNeverAppearOnScreen(t *testing.T) {
	const secret = "tui-secret-value-98765"
	t.Setenv("BARGE_TUI_SECRET", secret)
	y := strings.Replace(baseCfg, `{name: web, image: "nginx:1"}`, `{name: web, image: "nginx:1", secrets: {TOKEN: {env: BARGE_TUI_SECRET}}}`, 1)
	for _, w := range []int{16, 30, 100} {
		fx := newFixture(t, y)
		// A registry error that echoes the credential, as real ones sometimes do.
		fx.flaky.pullErr = errors.New("login failed for token " + secret + " (retry later)")
		fx.model.Update(tea.WindowSizeMsg{Width: w, Height: 60})
		fx.drive(fx.model.Init())
		for _, phase := range []func(){func() {}, func() { fx.press("a") }, func() { fx.press("y") }, func() { fx.press("r") }} {
			phase()
			s := fx.screen()
			// Wrapping must not be able to hide a leak, so also check with line breaks removed.
			flat := strings.ReplaceAll(strings.ReplaceAll(s, "\n", ""), " ", "")
			if strings.Contains(s, secret) || strings.Contains(flat, secret) || strings.Contains(flat, "tui-secret") {
				t.Fatalf("width %d: secret leaked:\n%s", w, s)
			}
		}
		if strings.Contains(fx.model.Summary(), secret) {
			t.Errorf("secret leaked into the summary: %q", fx.model.Summary())
		}
	}
}

func TestRenderingIsDeterministicWithoutAnimation(t *testing.T) {
	a := newFixture(t, baseCfg).start()
	b := newFixture(t, baseCfg).start()
	if a.screen() != b.screen() {
		t.Error("the same state must render identically")
	}
}

func TestTinyOrUnknownTerminalSizesDoNotPanic(t *testing.T) {
	for _, sz := range [][2]int{{0, 0}, {1, 1}, {5, 2}, {3, 100}} {
		fx := newFixture(t, baseCfg)
		fx.model.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		fx.drive(fx.model.Init())
		_ = fx.screen()
		fx.press("a")
		fx.press("y")
		_ = fx.screen()
	}
}

func TestScrollingPastTheEndDoesNotStoreHiddenOffset(t *testing.T) {
	fx := newFixture(t, manyApps(30))
	fx.model.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	fx.drive(fx.model.Init())
	for i := 0; i < 200; i++ {
		fx.press("down")
	}
	atEnd := fx.screen()
	fx.press("up")
	if fx.screen() == atEnd {
		t.Error("one 'up' after overscrolling must move the view immediately")
	}
}
