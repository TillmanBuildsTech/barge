package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/TillmanBuildsTech/barge/internal/app"
	"github.com/TillmanBuildsTech/barge/internal/tui"
)

type tuiProbe struct {
	called  bool
	cfg     tui.Config
	targets []string
	ret     int
	err     error
}

func (p *tuiProbe) run(ctx context.Context, cfg tui.Config) (int, error) {
	p.called, p.cfg = true, cfg
	for _, r := range cfg.Session.Plan(ctx) {
		p.targets = append(p.targets, r.Target.Name)
	}
	return p.ret, p.err
}

func runTUI(t *testing.T, e env, p *tuiProbe, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb strings.Builder
	a := app.New("v1.2.3", e.reg)
	a.SetTUIRunner(p.run)
	code = a.Run(t.Context(), append([]string{"tui"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func TestTUICommandWiresConfigVersionAndAllTargets(t *testing.T) {
	e := newEnv(t)
	cfg := cfgFile(t, strings.Replace(deployCfg, "targets:\n", "targets:\n  - {name: other, type: local, apps: [api]}\n", 1))
	p := &tuiProbe{}
	code, out, errs := runTUI(t, e, p, "-config", cfg)
	if code != 0 || !p.called {
		t.Fatalf("code=%d called=%v err=%s", code, p.called, errs)
	}
	if p.cfg.Version != "v1.2.3" || p.cfg.ConfigPath != cfg {
		t.Errorf("cfg = %+v", p.cfg)
	}
	if strings.Join(p.targets, ",") != "other,dev" && strings.Join(p.targets, ",") != "dev,other" {
		t.Errorf("targets = %v", p.targets)
	}
	if out != "" {
		t.Errorf("barge must not write to stdout around the TUI, got %q", out)
	}
}

func TestTUICommandHonoursTargetFlag(t *testing.T) {
	e := newEnv(t)
	cfg := cfgFile(t, strings.Replace(deployCfg, "targets:\n", "targets:\n  - {name: other, type: local, apps: [api]}\n", 1))
	p := &tuiProbe{}
	if code, _, errs := runTUI(t, e, p, "-config", cfg, "-target", "other"); code != 0 {
		t.Fatalf("code=%d %s", code, errs)
	}
	if strings.Join(p.targets, ",") != "other" {
		t.Errorf("targets = %v", p.targets)
	}
}

func TestTUICommandPropagatesTheExitCode(t *testing.T) {
	e := newEnv(t)
	p := &tuiProbe{ret: 1}
	if code, _, _ := runTUI(t, e, p, "-config", cfgFile(t, deployCfg)); code != 1 {
		t.Errorf("code = %d, want the TUI's own exit code", code)
	}
}

func TestTUICommandReportsRunnerErrors(t *testing.T) {
	e := newEnv(t)
	p := &tuiProbe{err: errors.New("terminal exploded")}
	code, _, errs := runTUI(t, e, p, "-config", cfgFile(t, deployCfg))
	if code != 1 || !strings.Contains(errs, "terminal exploded") {
		t.Errorf("code=%d stderr=%q", code, errs)
	}
}

func TestTUICommandValidatesBeforeStarting(t *testing.T) {
	e := newEnv(t)
	good := cfgFile(t, deployCfg)
	tests := []struct {
		name string
		args []string
		code int
		msg  string
	}{
		{"no config", nil, 2, "-config"},
		{"unknown flag", []string{"-bogus"}, 2, ""},
		{"missing file", []string{"-config", "/nonexistent.yaml"}, 1, "open config"},
		{"unknown driver", []string{"-config", cfgFile(t, strings.Replace(deployCfg, "driver: fake", "driver: rkt", 1))}, 1, "rkt"},
		{"unknown target", []string{"-config", good, "-target", "nope"}, 2, "nope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &tuiProbe{}
			code, _, errs := runTUI(t, e, p, tt.args...)
			if code != tt.code || !strings.Contains(errs, tt.msg) {
				t.Errorf("code=%d stderr=%q", code, errs)
			}
			if p.called {
				t.Error("the TUI must not start on invalid input")
			}
		})
	}
}

func TestTUIRefusesToRunWithoutATerminal(t *testing.T) {
	// Under `go test` stdin/stdout are not terminals, so the real runner must
	// refuse cleanly and point at the headless commands instead of hanging or
	// spraying escape codes into a pipe.
	e := newEnv(t)
	var out, errb strings.Builder
	code := app.New("test", e.reg).Run(t.Context(), []string{"tui", "-config", cfgFile(t, deployCfg)}, &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "terminal") || !strings.Contains(errb.String(), "plan") || !strings.Contains(errb.String(), "apply") {
		t.Errorf("code=%d stderr=%q", code, errb.String())
	}
	if out.Len() != 0 {
		t.Errorf("stdout must stay empty, got %q", out.String())
	}
}

func TestUsageMentionsTUI(t *testing.T) {
	_, _, errs := run(t)
	if !strings.Contains(errs, "tui") {
		t.Errorf("usage should list the tui command:\n%s", errs)
	}
}
