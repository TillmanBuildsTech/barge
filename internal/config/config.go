// Package config loads and validates barge's versioned YAML configuration.
//
// Loading is pure: it reads from an io.Reader and never touches the
// filesystem, the network or the environment, so it is trivially testable.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/TillmanBuildsTech/barge/internal/domain"
)

// SchemaVersion is the only config schema version this build understands.
const SchemaVersion = 1

// Target types.
const (
	TargetLocal = "local"
	TargetSSH   = "ssh"
)

// Defaults applied when a field is omitted.
const (
	DefaultSSHPort           = 22
	DefaultSSHConnectTimeout = 30 * time.Second
)

// Duration is a time.Duration that unmarshals from strings like "30s".
type Duration struct{ time.Duration }

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", s, err)
	}
	d.Duration = v
	return nil
}

// Config is the root of the configuration document.
type Config struct {
	Version int        `yaml:"version"`
	Runtime RuntimeRef `yaml:"runtime"`
	Apps    []App      `yaml:"apps"`
	Targets []Target   `yaml:"targets"`
}

// RuntimeRef selects a container runtime driver by name. Driver names are
// resolved against the runtime registry at startup, so new runtimes need no
// config schema change.
type RuntimeRef struct {
	Driver  string            `yaml:"driver"`
	Options map[string]string `yaml:"options"`
}

// App is a containerised application.
type App struct {
	Name    string            `yaml:"name"`
	Image   string            `yaml:"image"`
	Command []string          `yaml:"command"`
	Env     map[string]string `yaml:"env"`
	Labels  map[string]string `yaml:"labels"`
	Ports   []Port            `yaml:"ports"`
	Volumes []Volume          `yaml:"volumes"`
	Restart string            `yaml:"restart"`
}

// Port is a published port.
type Port struct {
	HostIP    string `yaml:"host_ip"`
	Host      int    `yaml:"host"`
	Container int    `yaml:"container"`
	Protocol  string `yaml:"protocol"`
}

// Volume is a mount.
type Volume struct {
	Source   string `yaml:"source"`
	Target   string `yaml:"target"`
	ReadOnly bool   `yaml:"read_only"`
}

// Target is somewhere apps get deployed: this machine, or a Linux host over SSH.
type Target struct {
	Name    string     `yaml:"name"`
	Type    string     `yaml:"type"`
	Runtime RuntimeRef `yaml:"runtime"` // optional; inherits the top-level runtime
	SSH     *SSH       `yaml:"ssh"`
	Apps    []string   `yaml:"apps"`
}

// SSH describes how to reach a remote target. There is deliberately no
// "insecure" switch and no default user.
type SSH struct {
	Host           string   `yaml:"host"`
	Port           int      `yaml:"port"`
	User           string   `yaml:"user"`
	IdentityFile   string   `yaml:"identity_file"`
	KnownHosts     string   `yaml:"known_hosts"` // empty = ~/.ssh/known_hosts
	ConnectTimeout Duration `yaml:"connect_timeout"`
}

// Spec converts the app into the runtime-agnostic domain spec.
func (a App) Spec() domain.ContainerSpec {
	s := domain.ContainerSpec{
		Name: a.Name, Image: a.Image, Command: a.Command,
		Env: a.Env, Labels: a.Labels, Restart: domain.RestartPolicy(a.Restart),
	}
	for _, p := range a.Ports {
		s.Ports = append(s.Ports, domain.PortMapping{HostIP: p.HostIP, Host: p.Host, Container: p.Container, Protocol: p.Protocol})
	}
	for _, v := range a.Volumes {
		s.Volumes = append(s.Volumes, domain.VolumeMount{Source: v.Source, Target: v.Target, ReadOnly: v.ReadOnly})
	}
	return s
}

// App looks up an app by name.
func (c *Config) App(name string) (App, bool) {
	for _, a := range c.Apps {
		if a.Name == name {
			return a, true
		}
	}
	return App{}, false
}

// LoadFile opens path and calls Load.
func LoadFile(path string) (*Config, error) {
	f, err := os.Open(path) //nolint:gosec // path is the user's explicit --config
	if err != nil {
		return nil, fmt.Errorf("open config: %w", err)
	}
	defer func() { _ = f.Close() }()
	cfg, err := Load(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Load decodes strictly (unknown fields are errors), applies defaults, then
// validates. Any returned validation error wraps domain.ErrInvalid.
func Load(r io.Reader) (*Config, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: config is empty: version is required", domain.ErrInvalid)
		}
		return nil, fmt.Errorf("%w: parse: %w", domain.ErrInvalid, err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: config must contain a single YAML document", domain.ErrInvalid)
	}

	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	for i := range c.Targets {
		t := &c.Targets[i]
		if t.Runtime.Driver == "" {
			t.Runtime = c.Runtime
		}
		if t.SSH != nil {
			if t.SSH.Port == 0 {
				t.SSH.Port = DefaultSSHPort
			}
			if t.SSH.ConnectTimeout.Duration == 0 {
				t.SSH.ConnectTimeout.Duration = DefaultSSHConnectTimeout
			}
		}
	}
}

// Validate checks the whole config without side effects and reports every
// problem at once. The error wraps domain.ErrInvalid.
func (c *Config) Validate() error {
	var errs []error
	bad := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf("%w: %s", domain.ErrInvalid, fmt.Sprintf(format, a...)))
	}

	if c.Version != SchemaVersion {
		bad("version must be %d (got %d)", SchemaVersion, c.Version)
	}
	if c.Runtime.Driver == "" {
		bad("runtime.driver is required (e.g. docker or podman)")
	}

	apps := map[string]bool{}
	for i, a := range c.Apps {
		if apps[a.Name] {
			bad("apps[%d]: duplicate app name %q", i, a.Name)
		}
		apps[a.Name] = true
		if err := a.Spec().Validate(); err != nil {
			errs = append(errs, fmt.Errorf("apps[%d] (%s): %w", i, a.Name, err))
		}
	}

	if len(c.Targets) == 0 {
		bad("targets: at least one target is required")
	}
	seen := map[string]bool{}
	for i, t := range c.Targets {
		if t.Name == "" {
			bad("targets[%d]: name is required", i)
		} else if seen[t.Name] {
			bad("targets[%d]: duplicate target name %q", i, t.Name)
		}
		seen[t.Name] = true

		switch t.Type {
		case TargetLocal:
			if t.SSH != nil {
				bad("targets[%d] (%s): ssh block not allowed on a local target", i, t.Name)
			}
		case TargetSSH:
			c.validateSSH(i, t, bad)
		default:
			bad("targets[%d] (%s): type %q must be %q or %q", i, t.Name, t.Type, TargetLocal, TargetSSH)
		}
		for _, an := range t.Apps {
			if !apps[an] {
				bad("targets[%d] (%s): references unknown app %q", i, t.Name, an)
			}
		}
	}
	return errors.Join(errs...)
}

func (c *Config) validateSSH(i int, t Target, bad func(string, ...any)) {
	if t.SSH == nil {
		bad("targets[%d] (%s): ssh block is required for ssh targets", i, t.Name)
		return
	}
	s := t.SSH
	if s.Host == "" {
		bad("targets[%d] (%s): ssh.host is required", i, t.Name)
	}
	if s.User == "" {
		bad("targets[%d] (%s): ssh.user is required", i, t.Name)
	}
	if s.Port < 1 || s.Port > 65535 {
		bad("targets[%d] (%s): ssh.port %d must be 1-65535", i, t.Name, s.Port)
	}
	if s.ConnectTimeout.Duration <= 0 {
		bad("targets[%d] (%s): ssh.connect_timeout must be positive", i, t.Name)
	}
}
