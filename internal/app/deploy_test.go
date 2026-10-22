package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/TillmanBuildsTech/barge/internal/app"
	"github.com/TillmanBuildsTech/barge/internal/domain"
	rt "github.com/TillmanBuildsTech/barge/internal/runtime"
	"github.com/TillmanBuildsTech/barge/internal/runtime/fake"
	"github.com/TillmanBuildsTech/barge/internal/transport"
	"github.com/TillmanBuildsTech/barge/internal/transport/sshtest"
)

// flaky is a runtime whose Pull always fails.
type flaky struct{ rt.Runtime }

func (flaky) Pull(context.Context, string) error { return errors.New("registry unreachable") }

type env struct {
	fake *fake.Runtime
	reg  *rt.Registry
}

func newEnv(t *testing.T) env {
	t.Helper()
	f := fake.New()
	reg := rt.NewRegistry()
	for name, factory := range map[string]rt.Factory{
		"fake":  func(rt.Deps) (rt.Runtime, error) { return f, nil },
		"flaky": func(rt.Deps) (rt.Runtime, error) { return flaky{f}, nil },
	} {
		if err := reg.Register(name, factory); err != nil {
			t.Fatal(err)
		}
	}
	return env{fake: f, reg: reg}
}

func (e env) run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb strings.Builder
	code = app.New("test", e.reg).Run(t.Context(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func (e env) containers(t *testing.T) []domain.ContainerState {
	t.Helper()
	cs, err := e.fake.List(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

const deployCfg = `
version: 1
runtime: {driver: fake}
deploy: {settle: 10ms}
apps:
  - name: web
    image: nginx:1.27
    env: {API_TOKEN: super-secret-value}
  - name: api
    image: api:1
targets:
  - {name: dev, type: local, apps: [web, api]}
`

func cfgFile(t *testing.T, body string) string { t.Helper(); return writeConfig(t, body) }

func TestPlanShowsChangesAndMutatesNothing(t *testing.T) {
	e := newEnv(t)
	code, out, errs := e.run(t, "plan", "-config", cfgFile(t, deployCfg))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	for _, want := range []string{"dev", "web", "api", "create"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if n := len(e.containers(t)); n != 0 {
		t.Errorf("plan created %d containers", n)
	}
}

func TestApplyDryRunEqualsPlan(t *testing.T) {
	e := newEnv(t)
	code, out, _ := e.run(t, "apply", "-dry-run", "-config", cfgFile(t, deployCfg))
	if code != 0 || !strings.Contains(out, "create") {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if n := len(e.containers(t)); n != 0 {
		t.Errorf("dry run created %d containers", n)
	}
}

func TestApplyDeploysThenIsIdempotent(t *testing.T) {
	e := newEnv(t)
	cfg := cfgFile(t, deployCfg)
	code, out, errs := e.run(t, "apply", "-config", cfg)
	if code != 0 {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errs)
	}
	cs := e.containers(t)
	if len(cs) != 2 {
		t.Fatalf("containers = %+v", cs)
	}
	for _, c := range cs {
		if c.Status != domain.StatusRunning || c.Labels[domain.LabelManagedBy] != domain.ManagedByValue {
			t.Errorf("container not deployed properly: %+v", c)
		}
	}
	if !strings.Contains(out, "applied") {
		t.Errorf("output should report applied:\n%s", out)
	}

	code, out, _ = e.run(t, "apply", "-config", cfg)
	if code != 0 || strings.Contains(out, "applied") || !strings.Contains(out, "unchanged") {
		t.Errorf("second apply should be all unchanged: code=%d\n%s", code, out)
	}
}

func TestOutputNeverContainsEnvValues(t *testing.T) {
	e := newEnv(t)
	cfg := cfgFile(t, deployCfg)
	for _, args := range [][]string{
		{"plan", "-config", cfg}, {"plan", "-json", "-config", cfg},
		{"apply", "-config", cfg}, {"apply", "-json", "-config", cfg},
	} {
		_, out, errs := e.run(t, args...)
		if strings.Contains(out+errs, "super-secret-value") {
			t.Errorf("%v leaked an env value:\n%s%s", args, out, errs)
		}
	}
}

type jsonReport struct {
	Schema  int    `json:"schema"`
	Command string `json:"command"`
	DryRun  bool   `json:"dry_run"`
	OK      bool   `json:"ok"`
	Targets []struct {
		Name    string `json:"name"`
		Runtime string `json:"runtime"`
		Error   string `json:"error"`
		Apps    []struct {
			App      string   `json:"app"`
			Action   string   `json:"action"`
			Reason   string   `json:"reason"`
			Outcome  string   `json:"outcome"`
			Error    string   `json:"error"`
			Warnings []string `json:"warnings"`

			RolledBack    bool   `json:"rolled_back"`
			RollbackError string `json:"rollback_error"`
		} `json:"apps"`
	} `json:"targets"`
}

func parseJSON(t *testing.T, out string) jsonReport {
	t.Helper()
	var r jsonReport
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		t.Fatalf("stdout is not a single valid JSON document: %v\n%s", err, out)
	}
	if dec.More() {
		t.Fatalf("trailing data after JSON document:\n%s", out)
	}
	return r
}

func TestPlanJSON(t *testing.T) {
	e := newEnv(t)
	code, out, _ := e.run(t, "plan", "-json", "-config", cfgFile(t, deployCfg))
	r := parseJSON(t, out)
	if code != 0 || r.Schema != 1 || r.Command != "plan" || !r.OK {
		t.Fatalf("code=%d report=%+v", code, r)
	}
	tg := r.Targets[0]
	if tg.Name != "dev" || tg.Runtime != "fake" || len(tg.Apps) != 2 || tg.Apps[0].App != "web" || tg.Apps[0].Action != "create" || tg.Apps[0].Reason == "" {
		t.Errorf("target = %+v", tg)
	}
}

func TestApplyJSON(t *testing.T) {
	e := newEnv(t)
	code, out, _ := e.run(t, "apply", "-json", "-config", cfgFile(t, deployCfg))
	r := parseJSON(t, out)
	if code != 0 || r.Command != "apply" || !r.OK || r.Targets[0].Apps[0].Outcome != "applied" {
		t.Fatalf("code=%d report=%+v", code, r)
	}
}

func TestApplyFailureExitsNonZeroAndReportsIt(t *testing.T) {
	e := newEnv(t)
	cfg := cfgFile(t, strings.Replace(deployCfg, "runtime: {driver: fake}", "runtime: {driver: flaky}", 1))
	code, out, _ := e.run(t, "apply", "-config", cfg)
	if code != 1 || !strings.Contains(out, "FAILED") || !strings.Contains(out, "registry unreachable") {
		t.Errorf("code=%d out=%s", code, out)
	}
	code, out, _ = e.run(t, "apply", "-json", "-config", cfg)
	r := parseJSON(t, out)
	if code != 1 || r.OK || r.Targets[0].Apps[0].Outcome != "failed" || !strings.Contains(r.Targets[0].Apps[0].Error, "registry unreachable") {
		t.Errorf("code=%d report=%+v", code, r)
	}
}

func TestBlockedPlanFailsAndApplyChangesNothing(t *testing.T) {
	e := newEnv(t)
	// A container named "api" that barge did not create.
	if _, err := e.fake.Create(t.Context(), domain.ContainerSpec{Name: "api", Image: "theirs:1"}); err != nil {
		t.Fatal(err)
	}
	cfg := cfgFile(t, deployCfg)

	code, out, _ := e.run(t, "plan", "-config", cfg)
	if code != 1 || !strings.Contains(out, "not managed by barge") {
		t.Errorf("plan: code=%d\n%s", code, out)
	}
	code, _, _ = e.run(t, "apply", "-config", cfg)
	if code != 1 {
		t.Errorf("apply: code=%d", code)
	}
	if cs := e.containers(t); len(cs) != 1 || cs[0].Image != "theirs:1" {
		t.Errorf("a blocked apply must not touch anything, even the unblocked app: %+v", cs)
	}
}

func TestTargetSelection(t *testing.T) {
	e := newEnv(t)
	two := strings.Replace(deployCfg, "targets:\n", "targets:\n  - {name: other, type: local, apps: [api]}\n", 1)
	cfg := cfgFile(t, two)

	code, out, _ := e.run(t, "plan", "-target", "other", "-config", cfg)
	if code != 0 || strings.Contains(out, "web") || !strings.Contains(out, "api") {
		t.Errorf("code=%d\n%s", code, out)
	}
	code, _, errs := e.run(t, "plan", "-target", "nope", "-config", cfg)
	if code != 2 || !strings.Contains(errs, "nope") || !strings.Contains(errs, "dev") {
		t.Errorf("unknown target should be a usage error listing known targets: code=%d %q", code, errs)
	}
}

func TestDeployCommandsValidateInput(t *testing.T) {
	e := newEnv(t)
	for _, cmd := range []string{"plan", "apply"} {
		if code, _, errs := e.run(t, cmd); code != 2 || !strings.Contains(errs, "-config") {
			t.Errorf("%s without -config: code=%d %q", cmd, code, errs)
		}
		if code, _, _ := e.run(t, cmd, "-config", "/nonexistent.yaml"); code != 1 {
			t.Errorf("%s with missing file: code=%d", cmd, code)
		}
		if code, _, _ := e.run(t, cmd, "-bogus"); code != 2 {
			t.Errorf("%s with unknown flag: code=%d", cmd, code)
		}
	}
	bad := cfgFile(t, strings.Replace(deployCfg, "driver: fake", "driver: rkt", 1))
	if code, _, errs := e.run(t, "plan", "-config", bad); code != 1 || !strings.Contains(errs, "rkt") {
		t.Errorf("unknown driver: code=%d %q", code, errs)
	}
}

// ---- ssh wiring ---------------------------------------------------------

func sshCfg(t *testing.T, host string, port int, identity, knownHosts string) string {
	t.Helper()
	return cfgFile(t, fmt.Sprintf(`
version: 1
runtime: {driver: probe}
deploy: {settle: 10ms}
apps:
  - {name: web, image: "nginx:1"}
targets:
  - name: prod
    type: ssh
    ssh: {host: %s, port: %d, user: deploy, identity_file: %q, known_hosts: %q}
    apps: [web]
`, host, port, identity, knownHosts))
}

func TestSSHTargetRunsRuntimeCommandsOverVerifiedSSH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test sshd needs a POSIX sh")
	}
	keyPath, pub := sshtest.NewClientKey(t)
	srv := sshtest.New(t, "deploy", pub)
	kh := sshtest.WriteKnownHosts(t, srv.Host(), srv.Port(), srv.HostKey())

	var (
		mu     sync.Mutex
		probed string
	)
	f := fake.New()
	reg := rt.NewRegistry()
	_ = reg.Register("probe", func(d rt.Deps) (rt.Runtime, error) {
		// Prove the executor handed to the driver really reaches the remote host.
		res, err := d.Exec.Run(context.Background(), transport.Cmd{Argv: []string{"printf", "%s", "from-remote"}})
		mu.Lock()
		probed = string(res.Stdout)
		mu.Unlock()
		return f, err
	})

	var out, errb strings.Builder
	code := app.New("test", reg).Run(t.Context(), []string{"plan", "-config", sshCfg(t, srv.Host(), srv.Port(), keyPath, kh)}, &out, &errb)
	if code != 0 {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errb.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if probed != "from-remote" {
		t.Errorf("driver executor did not reach the ssh host, got %q", probed)
	}
}

func TestSSHTargetWithBadHostKeyFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test sshd needs a POSIX sh")
	}
	keyPath, pub := sshtest.NewClientKey(t)
	srv := sshtest.New(t, "deploy", pub)
	_, other := sshtest.NewClientKey(t)
	kh := sshtest.WriteKnownHosts(t, srv.Host(), srv.Port(), other) // wrong key

	e := newEnv(t)
	_ = e.reg.Register("probe", func(d rt.Deps) (rt.Runtime, error) {
		if _, err := d.Exec.Run(context.Background(), transport.Cmd{Argv: []string{"true"}}); err != nil {
			return nil, err
		}
		return e.fake, nil
	})
	code, out, errs := e.run(t, "apply", "-config", sshCfg(t, srv.Host(), srv.Port(), keyPath, kh))
	if code != 1 || !strings.Contains(out+errs, "prod") || !strings.Contains(out+errs, "host key") {
		t.Errorf("code=%d out=%s err=%s", code, out, errs)
	}
	if cmds := srv.Commands(); len(cmds) != 0 {
		t.Errorf("nothing may run on an unverified host: %v", cmds)
	}
}

func TestOneUnreachableTargetBlocksApplyEverywhere(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	missingKey := filepath.Join(dir, "no-such-key")
	_ = os.WriteFile(filepath.Join(dir, "known_hosts"), nil, 0o600)
	body := strings.Replace(deployCfg, "targets:\n", fmt.Sprintf(`targets:
  - name: remote
    type: ssh
    ssh: {host: 127.0.0.1, port: 1, user: deploy, identity_file: %q, known_hosts: %q}
    apps: [api]
`, missingKey, filepath.Join(dir, "known_hosts")), 1)

	code, out, errs := e.run(t, "apply", "-config", cfgFile(t, body))
	if code != 1 || !strings.Contains(out+errs, "remote") {
		t.Errorf("code=%d out=%s err=%s", code, out, errs)
	}
	if cs := e.containers(t); len(cs) != 0 {
		t.Errorf("fail closed: the healthy target must not be deployed when another cannot be planned: %+v", cs)
	}
}
