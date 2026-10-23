package config_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/TillmanBuildsTech/barge/internal/domain"
)

func TestRuntimeOptionsAreRejectedUntilADriverUsesThem(t *testing.T) {
	top := strings.Replace(validYAML, "runtime:\n  driver: docker\n", "runtime:\n  driver: docker\n  options: {socket: /x}\n", 1)
	perTarget := strings.Replace(validYAML, "    runtime:\n      driver: podman\n", "    runtime:\n      driver: podman\n      options: {root: /x}\n", 1)
	for name, y := range map[string]string{"top level": top, "per target": perTarget} {
		_, err := load(t, y)
		if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "options") || !strings.Contains(err.Error(), "not supported") {
			t.Errorf("%s: want ErrInvalid saying options are not supported, got %v", name, err)
		}
	}
}

func TestDuplicateAppInOneTargetIsRejected(t *testing.T) {
	y := strings.Replace(validYAML, "    apps: [web]\n  - name: prod", "    apps: [web, web]\n  - name: prod", 1)
	_, err := load(t, y)
	if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), `lists app "web" more than once`) {
		t.Fatalf("got %v", err)
	}
}

func TestReservedTemporarySuffixesAreRejectedInAppNames(t *testing.T) {
	for _, suffix := range []string{"-barge-old", "-barge-new"} {
		y := strings.Replace(validYAML, "- name: web\n", "- name: web"+suffix+"\n", 1)
		y = strings.ReplaceAll(y, "apps: [web]", "apps: [web"+suffix+"]")
		_, err := load(t, y)
		if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), suffix) {
			t.Errorf("%s: got %v", suffix, err)
		}
	}
}

func TestUnusedApps(t *testing.T) {
	y := strings.Replace(validYAML, "targets:", "  - name: zeta\n    image: z:1\n  - name: alpha\n    image: a:1\ntargets:", 1)
	cfg, err := load(t, y)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.UnusedApps(), ","); got != "alpha,zeta" {
		t.Errorf("UnusedApps() = %q", got)
	}
	cfg, _ = load(t, validYAML)
	if len(cfg.UnusedApps()) != 0 {
		t.Errorf("UnusedApps() = %v, want none", cfg.UnusedApps())
	}
}
