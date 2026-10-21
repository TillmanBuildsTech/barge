package config_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/TillmanBuildsTech/barge/internal/config"
	"github.com/TillmanBuildsTech/barge/internal/domain"
)

const validYAML = `
version: 1
runtime:
  driver: docker
apps:
  - name: web
    image: nginx:1.27
    ports:
      - host: 8080
        container: 80
    env:
      LOG_LEVEL: info
    restart: unless-stopped
targets:
  - name: dev
    type: local
    apps: [web]
  - name: prod
    type: ssh
    runtime:
      driver: podman
    ssh:
      host: prod.example.com
      user: deploy
      identity_file: ~/.ssh/id_ed25519
    apps: [web]
`

func load(t *testing.T, y string) (*config.Config, error) {
	t.Helper()
	return config.Load(strings.NewReader(y))
}

func TestLoadValid(t *testing.T) {
	cfg, err := load(t, validYAML)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.Targets[0].Runtime.Driver; got != "docker" {
		t.Errorf("local target should inherit default runtime docker, got %q", got)
	}
	if got := cfg.Targets[1].Runtime.Driver; got != "podman" {
		t.Errorf("prod target override lost, got %q", got)
	}
	ssh := cfg.Targets[1].SSH
	if ssh.Port != 22 {
		t.Errorf("default ssh port = %d, want 22", ssh.Port)
	}
	if ssh.ConnectTimeout.Duration != 30*time.Second {
		t.Errorf("default connect timeout = %v, want 30s", ssh.ConnectTimeout.Duration)
	}
	app, ok := cfg.App("web")
	if !ok || app.Image != "nginx:1.27" {
		t.Fatalf("App(web) = %+v, %v", app, ok)
	}
	spec := app.Spec()
	if spec.Name != "web" || spec.Restart != domain.RestartUnlessStopped || len(spec.Ports) != 1 {
		t.Errorf("Spec() = %+v", spec)
	}
}

func TestLoadRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"empty document", "", "version"},
		{"missing version", strings.Replace(validYAML, "version: 1", "", 1), "version"},
		{"unsupported version", strings.Replace(validYAML, "version: 1", "version: 2", 1), "version"},
		{"unknown top-level field", validYAML + "\nbogus: true\n", "bogus"},
		{"typo in nested field", strings.Replace(validYAML, "identity_file", "identity_flie", 1), "identity_flie"},
		{"missing default driver", strings.Replace(validYAML, "runtime:\n  driver: docker\n", "", 1), "runtime.driver"},
		{"no targets", "version: 1\nruntime:\n  driver: docker\n", "targets"},
		{"duplicate target", validYAML + "  - name: dev\n    type: local\n", "duplicate"},
		{"unknown target type", strings.Replace(validYAML, "type: local", "type: carrier-pigeon", 1), "type"},
		{"ssh target without ssh block", strings.Replace(validYAML, "    ssh:\n      host: prod.example.com\n      user: deploy\n      identity_file: ~/.ssh/id_ed25519\n", "", 1), "ssh"},
		{"ssh without user", strings.Replace(validYAML, "      user: deploy\n", "", 1), "user"},
		{"ssh bad port", strings.Replace(validYAML, "host: prod.example.com", "host: prod.example.com\n      port: 99999", 1), "port"},
		{"local target with ssh block", strings.Replace(validYAML, "type: local", "type: local\n    ssh: {host: x, user: y}", 1), "ssh"},
		{"target references unknown app", strings.Replace(validYAML, "apps: [web]", "apps: [ghost]", 1), "ghost"},
		{"duplicate app", strings.Replace(validYAML, "apps:\n  - name: web", "apps:\n  - name: web\n    image: x\n  - name: web", 1), "duplicate"},
		{"invalid app spec surfaces domain error", strings.Replace(validYAML, "image: nginx:1.27", "image: --privileged", 1), "image"},
		{"negative timeout", strings.Replace(validYAML, "host: prod.example.com", "host: prod.example.com\n      connect_timeout: -5s", 1), "connect_timeout"},
		{"malformed yaml", "version: [", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(t, tt.yaml)
			if err == nil {
				t.Fatal("Load() = nil error, want failure")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestLoadInvalidWrapsErrInvalid(t *testing.T) {
	_, err := load(t, strings.Replace(validYAML, "version: 1", "version: 2", 1))
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want errors.Is(err, ErrInvalid), got %v", err)
	}
}

func TestLoadReportsAllProblemsAtOnce(t *testing.T) {
	y := "version: 1\nruntime:\n  driver: docker\ntargets:\n  - name: a\n    type: nope\n  - name: b\n    type: ssh\n"
	_, err := load(t, y)
	if err == nil {
		t.Fatal("want error")
	}
	for _, s := range []string{"targets[0]", "targets[1]"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error should mention %s: %v", s, err)
		}
	}
}

func FuzzLoadNeverPanics(f *testing.F) {
	f.Add(validYAML)
	f.Add("version: 1")
	f.Add("{{{{")
	f.Fuzz(func(t *testing.T, in string) {
		_, _ = config.Load(strings.NewReader(in))
	})
}
