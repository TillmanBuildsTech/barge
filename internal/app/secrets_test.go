package app_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/TillmanBuildsTech/barge/internal/app"
	"github.com/TillmanBuildsTech/barge/internal/domain"
	rt "github.com/TillmanBuildsTech/barge/internal/runtime"
	"github.com/TillmanBuildsTech/barge/internal/runtime/fake"
)

const (
	envSecret  = "env-secret-value-1"
	fileSecret = "file-secret-value-2"
)

// recorder remembers every spec it was asked to create and can fail pulls.
type recorder struct {
	rt.Runtime
	mu      sync.Mutex
	created []domain.ContainerSpec
	pullErr error
}

func (r *recorder) Create(ctx context.Context, s domain.ContainerSpec) (string, error) {
	r.mu.Lock()
	r.created = append(r.created, s)
	r.mu.Unlock()
	return r.Runtime.Create(ctx, s)
}

func (r *recorder) Pull(ctx context.Context, ref string) error {
	if r.pullErr != nil {
		return r.pullErr
	}
	return r.Runtime.Pull(ctx, ref)
}

func (r *recorder) lastEnv() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.created[len(r.created)-1].Env
}

type secEnv struct {
	rec  *recorder
	reg  *rt.Registry
	cfg  string // config path
	file string // path of the file secret
}

func newSecEnv(t *testing.T, extraTargets string) secEnv {
	t.Helper()
	rec := &recorder{Runtime: fake.New()}
	reg := rt.NewRegistry()
	if err := reg.Register("rec", func(rt.Deps) (rt.Runtime, error) { return rec, nil }); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "api_key")
	if err := os.WriteFile(file, []byte(fileSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := writeConfig(t, fmt.Sprintf(`
version: 1
runtime: {driver: rec}
deploy: {settle: 10ms}
apps:
  - name: web
    image: nginx:1
    env: {LOG_LEVEL: info}
    secrets:
      DB_PASSWORD: {env: BARGE_T_DBPASS}
      API_KEY: {file: %q}
targets:
  - {name: dev, type: local, apps: [web]}
%s`, file, extraTargets))
	return secEnv{rec: rec, reg: reg, cfg: cfg, file: file}
}

func (e secEnv) run(t *testing.T, args ...string) (code int, out, errs string) {
	t.Helper()
	var o, er strings.Builder
	code = app.New("test", e.reg).Run(t.Context(), args, &o, &er)
	return code, o.String(), er.String()
}

func noLeak(t *testing.T, what string, texts ...string) {
	t.Helper()
	for _, s := range texts {
		for _, secret := range []string{envSecret, fileSecret} {
			if strings.Contains(s, secret) {
				t.Errorf("%s leaked %q:\n%s", what, secret, s)
			}
		}
	}
}

func TestSecretsReachTheRuntimeButNeverTheOutput(t *testing.T) {
	t.Setenv("BARGE_T_DBPASS", envSecret)
	e := newSecEnv(t, "")

	for _, args := range [][]string{
		{"plan"}, {"plan", "-json"}, {"apply"}, {"apply", "-json"}, {"apply"}, {"apply", "-dry-run"},
	} {
		code, out, errs := e.run(t, append(args, "-config", e.cfg)...)
		if code != 0 {
			t.Fatalf("%v: code=%d out=%s err=%s", args, code, out, errs)
		}
		noLeak(t, fmt.Sprint(args), out, errs)
	}
	env := e.rec.lastEnv()
	if env["DB_PASSWORD"] != envSecret || env["API_KEY"] != fileSecret || env["LOG_LEVEL"] != "info" {
		t.Errorf("runtime received env %v; want the resolved secrets (file value without its trailing newline)", env)
	}
}

func TestRotatingASecretRedeploys(t *testing.T) {
	t.Setenv("BARGE_T_DBPASS", envSecret)
	e := newSecEnv(t, "")
	if code, out, errs := e.run(t, "apply", "-config", e.cfg); code != 0 {
		t.Fatalf("code=%d %s %s", code, out, errs)
	}
	if code, out, _ := e.run(t, "plan", "-config", e.cfg); code != 0 || !strings.Contains(out, "unchanged") {
		t.Fatalf("unchanged secret should plan as unchanged: code=%d\n%s", code, out)
	}

	t.Setenv("BARGE_T_DBPASS", "rotated-"+envSecret)
	code, out, _ := e.run(t, "plan", "-config", e.cfg)
	if code != 0 || !strings.Contains(out, "replace") {
		t.Fatalf("a rotated secret must plan as replace: code=%d\n%s", code, out)
	}
	if code, _, errs := e.run(t, "apply", "-config", e.cfg); code != 0 {
		t.Fatalf("apply: %d %s", code, errs)
	}
	if got := e.rec.lastEnv()["DB_PASSWORD"]; got != "rotated-"+envSecret {
		t.Errorf("new container got %q", got)
	}
	_, out, errs := e.run(t, "plan", "-config", e.cfg)
	noLeak(t, "plan after rotation", out, errs)
	if strings.Contains(out+errs, "rotated-") {
		t.Error("the rotated value leaked")
	}
}

func TestMissingSecretFailsClosedAndNamesWhatToFix(t *testing.T) {
	os.Unsetenv("BARGE_T_DBPASS") //nolint:errcheck // making sure it is absent
	e := newSecEnv(t, "")

	code, out, errs := e.run(t, "plan", "-config", e.cfg)
	if code != 1 || !strings.Contains(out+errs, "DB_PASSWORD") || !strings.Contains(out+errs, "BARGE_T_DBPASS") {
		t.Errorf("plan: code=%d\n%s%s", code, out, errs)
	}
	code, _, _ = e.run(t, "apply", "-config", e.cfg)
	if code != 1 {
		t.Errorf("apply: code=%d", code)
	}
	if len(e.rec.created) != 0 {
		t.Errorf("an unresolvable secret must stop the deploy before anything runs: %+v", e.rec.created)
	}
}

func TestMissingFileSecretIsReported(t *testing.T) {
	t.Setenv("BARGE_T_DBPASS", envSecret)
	e := newSecEnv(t, "")
	if err := os.Remove(e.file); err != nil {
		t.Fatal(err)
	}
	code, out, errs := e.run(t, "plan", "-config", e.cfg)
	if code != 1 || !strings.Contains(out+errs, "API_KEY") || !strings.Contains(out+errs, filepath.Base(e.file)) {
		t.Errorf("code=%d\n%s%s", code, out, errs)
	}
}

func TestOnlySecretsOfSelectedTargetsAreNeeded(t *testing.T) {
	os.Unsetenv("BARGE_T_DBPASS") //nolint:errcheck
	e := newSecEnv(t, "  - {name: other, type: local, apps: [plain]}\n")
	// A second app with no secrets, deployed to its own target.
	cfg := strings.Replace(mustRead(t, e.cfg), "targets:\n", "  - {name: plain, image: \"alpine:3\"}\ntargets:\n", 1)
	if err := os.WriteFile(e.cfg, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := e.run(t, "plan", "-target", "other", "-config", e.cfg); code != 0 {
		t.Errorf("target without secrets must not need other targets' secrets: code=%d\n%s%s", code, out, errs)
	}
	if code, _, _ := e.run(t, "plan", "-target", "dev", "-config", e.cfg); code != 1 {
		t.Errorf("the target that needs the secret must fail: code=%d", code)
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Defence in depth: text that did not come from barge (a registry or CLI error
// that echoes credentials) is scrubbed before it reaches the terminal or JSON.
func TestKnownSecretValuesAreRedactedFromRuntimeErrors(t *testing.T) {
	t.Setenv("BARGE_T_DBPASS", envSecret)
	e := newSecEnv(t, "")
	e.rec.pullErr = errors.New("registry rejected login: password=" + envSecret + " token=" + fileSecret)

	for _, args := range [][]string{{"apply"}, {"apply", "-json"}} {
		code, out, errs := e.run(t, append(args, "-config", e.cfg)...)
		if code != 1 {
			t.Fatalf("%v: code=%d", args, code)
		}
		noLeak(t, fmt.Sprint(args), out, errs)
		if !strings.Contains(out, "***") || !strings.Contains(out, "registry rejected login") {
			t.Errorf("%v: expected a masked message, got:\n%s", args, out)
		}
	}
}

func TestValidateStaysOfflineUnlessAskedToResolveSecrets(t *testing.T) {
	os.Unsetenv("BARGE_T_DBPASS") //nolint:errcheck
	e := newSecEnv(t, "")

	if code, out, errs := e.run(t, "validate", "-config", e.cfg); code != 0 {
		t.Errorf("plain validate must not need secrets to exist: code=%d %s %s", code, out, errs)
	}
	code, _, errs := e.run(t, "validate", "-resolve-secrets", "-config", e.cfg)
	if code != 1 || !strings.Contains(errs, "DB_PASSWORD") || !strings.Contains(errs, "BARGE_T_DBPASS") {
		t.Errorf("code=%d %s", code, errs)
	}

	t.Setenv("BARGE_T_DBPASS", envSecret)
	code, out, errs := e.run(t, "validate", "-resolve-secrets", "-config", e.cfg)
	if code != 0 || !strings.Contains(out, "2 secrets") {
		t.Errorf("code=%d out=%s err=%s", code, out, errs)
	}
	noLeak(t, "validate -resolve-secrets", out, errs)
}
