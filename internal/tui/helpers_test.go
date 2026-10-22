package tui_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/TillmanBuildsTech/barge/internal/config"
	"github.com/TillmanBuildsTech/barge/internal/domain"
	rt "github.com/TillmanBuildsTech/barge/internal/runtime"
	"github.com/TillmanBuildsTech/barge/internal/runtime/fake"
	"github.com/TillmanBuildsTech/barge/internal/session"
	"github.com/TillmanBuildsTech/barge/internal/tui"
)

// flaky wraps a runtime with injectable failures.
type flaky struct {
	rt.Runtime
	mu         sync.Mutex
	pullErr    error
	startFails int // the first N Start calls fail
}

func (f *flaky) Pull(ctx context.Context, ref string) error {
	if f.pullErr != nil {
		return f.pullErr
	}
	return f.Runtime.Pull(ctx, ref)
}

func (f *flaky) Start(ctx context.Context, id string) error {
	f.mu.Lock()
	fail := f.startFails > 0
	if fail {
		f.startFails--
	}
	f.mu.Unlock()
	if fail {
		return errors.New("injected start failure")
	}
	return f.Runtime.Start(ctx, id)
}

type fixture struct {
	t     *testing.T
	fake  *fake.Runtime
	flaky *flaky
	sess  *session.Session
	model *tui.Model
}

const baseCfg = `
version: 1
runtime: {driver: fake}
deploy: {settle: 5ms}
apps:
  - {name: web, image: "nginx:1"}
  - {name: api, image: "api:1"}
targets:
  - {name: dev, type: local, apps: [web, api]}
`

func newFixture(t *testing.T, yaml string) *fixture {
	t.Helper()
	cfg, err := config.Load(strings.NewReader(yaml))
	if err != nil {
		t.Fatal(err)
	}
	f := fake.New()
	fl := &flaky{Runtime: f}
	reg := rt.NewRegistry()
	if err := reg.Register("fake", func(rt.Deps) (rt.Runtime, error) { return fl, nil }); err != nil {
		t.Fatal(err)
	}
	sess := session.New(cfg, reg, cfg.Targets)
	t.Cleanup(func() { _ = sess.Close() })
	fx := &fixture{t: t, fake: f, flaky: fl, sess: sess}
	fx.model = tui.New(tui.Config{Version: "v9.9.9", ConfigPath: "barge.yaml", Session: sess, NoAnimation: true, Context: t.Context()})
	return fx
}

// drive executes cmd and everything it produces, feeding results back into the
// model, until no work is left. Quit is reported through the return value.
func (fx *fixture) drive(cmd tea.Cmd) (quit bool) {
	fx.t.Helper()
	pending := []tea.Cmd{cmd}
	for steps := 0; len(pending) > 0; steps++ {
		if steps > 10000 {
			fx.t.Fatal("drive: runaway command loop")
		}
		c := pending[0]
		pending = pending[1:]
		if c == nil {
			continue
		}
		msg := c()
		switch m := msg.(type) {
		case nil:
		case tea.QuitMsg:
			quit = true
		case tea.BatchMsg:
			pending = append(pending, m...)
		default:
			_, next := fx.model.Update(msg)
			pending = append(pending, next)
		}
	}
	return quit
}

// start runs Init to completion (the first plan).
func (fx *fixture) start() *fixture {
	fx.t.Helper()
	fx.model.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	fx.drive(fx.model.Init())
	return fx
}

func key(s string) tea.Msg {
	switch s {
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	}
	r := []rune(s)
	return tea.KeyPressMsg{Code: r[0], Text: s}
}

// press sends a key and drives whatever it triggers.
func (fx *fixture) press(s string) (quit bool) {
	fx.t.Helper()
	_, cmd := fx.model.Update(key(s))
	return fx.drive(cmd)
}

// screen returns the current view with styling removed.
func (fx *fixture) screen() string { return ansi.Strip(fx.model.View().Content) }

func (fx *fixture) contains(want ...string) {
	fx.t.Helper()
	s := fx.screen()
	for _, w := range want {
		if !strings.Contains(s, w) {
			fx.t.Errorf("screen is missing %q:\n%s", w, s)
		}
	}
}

func (fx *fixture) lacks(unwanted ...string) {
	fx.t.Helper()
	s := fx.screen()
	for _, w := range unwanted {
		if strings.Contains(s, w) {
			fx.t.Errorf("screen must not contain %q:\n%s", w, s)
		}
	}
}

func (fx *fixture) containers() []domain.ContainerState {
	fx.t.Helper()
	cs, err := fx.fake.List(context.Background(), nil)
	if err != nil {
		fx.t.Fatal(err)
	}
	return cs
}

func names(cs []domain.ContainerState) string {
	n := make([]string, 0, len(cs))
	for _, c := range cs {
		n = append(n, fmt.Sprintf("%s=%s", c.Name, c.Status))
	}
	return strings.Join(n, ",")
}

func contextWithCancel(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithCancel(t.Context())
}
