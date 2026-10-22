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
	"regexp"
	"sort"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/TillmanBuildsTech/barge/internal/domain"
)

var envVarRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

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
	DefaultSettle            = 5 * time.Second
	DefaultStopTimeout       = 10 * time.Second
)

// Duration is a time.Duration that unmarshals from strings like "30s".
type Duration struct{ time.Duration }

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return fmt.Errorf("duration must be a string like \"30s\": %w", err)
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
	Deploy  Deploy     `yaml:"deploy"`
	Apps    []App      `yaml:"apps"`
	Targets []Target   `yaml:"targets"`
}

// Deploy tunes how replacements are rolled out.
type Deploy struct {
	// Settle is how long a started container must stay running before the
	// deploy is considered successful; otherwise it is rolled back.
	Settle Duration `yaml:"settle"`
	// StopTimeout is the grace period given to the old container to stop.
	StopTimeout Duration `yaml:"stop_timeout"`
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
	// Secrets maps container env var names to references; see SecretRef.
	Secrets map[string]SecretRef `yaml:"secrets"`
}

// SecretRef points at a secret value without containing it. Exactly one of Env
// (the name of an environment variable in barge's own environment) or File (a
// path to a file holding the value) must be set. Resolution happens at plan
// time, never while loading the config.
type SecretRef struct {
	Env  string `yaml:"env"`
	File string `yaml:"file"`
}

// UnmarshalYAML decodes a reference strictly and by hand. The default decoder
// would echo a mistakenly inlined literal into its error message, which would
// end up in logs; this one never repeats a value. (yaml.v3 also does not apply
// KnownFields inside custom unmarshalers, so unknown keys are checked here.)
func (r *SecretRef) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: secrets must be references such as {env: NAME} or {file: PATH}; "+
			"literal values are not allowed in the config", n.Line)
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		if seen[key.Value] {
			return fmt.Errorf("line %d: duplicate key %q in a secret reference", key.Line, key.Value)
		}
		seen[key.Value] = true
		var dst *string
		switch key.Value {
		case "env":
			dst = &r.Env
		case "file":
			dst = &r.File
		default:
			return fmt.Errorf("line %d: unknown field %q in a secret reference (allowed: env, file)", key.Line, key.Value)
		}
		if val.Kind != yaml.ScalarNode {
			return fmt.Errorf("line %d: %q must be a string", val.Line, key.Value)
		}
		*dst = val.Value
	}
	return nil
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

// SecretNames returns the app's secret names in sorted order.
func (a App) SecretNames() []string {
	names := make([]string, 0, len(a.Secrets))
	for k := range a.Secrets {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
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
	if c.Deploy.Settle.Duration == 0 {
		c.Deploy.Settle.Duration = DefaultSettle
	}
	if c.Deploy.StopTimeout.Duration == 0 {
		c.Deploy.StopTimeout.Duration = DefaultStopTimeout
	}
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

	if c.Deploy.Settle.Duration < 0 {
		bad("deploy.settle must not be negative")
	}
	if c.Deploy.StopTimeout.Duration < 0 {
		bad("deploy.stop_timeout must not be negative")
	}

	apps := map[string]bool{}
	for i, a := range c.Apps {
		if apps[a.Name] {
			bad("apps[%d]: duplicate app name %q", i, a.Name)
		}
		apps[a.Name] = true
		for k := range a.Labels {
			if domain.ReservedLabel(k) {
				bad("apps[%d] (%s): label %q is reserved for barge (managed-by, barge.*)", i, a.Name, k)
			}
		}
		for _, sn := range a.SecretNames() {
			validateSecret(a, i, sn, bad)
		}
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
	if s.IdentityFile == "" {
		bad("targets[%d] (%s): ssh.identity_file is required (ssh-agent support is not implemented yet)", i, t.Name)
	}
	if s.Port < 1 || s.Port > 65535 {
		bad("targets[%d] (%s): ssh.port %d must be 1-65535", i, t.Name, s.Port)
	}
	if s.ConnectTimeout.Duration <= 0 {
		bad("targets[%d] (%s): ssh.connect_timeout must be positive", i, t.Name)
	}
}

func validateSecret(a App, i int, name string, bad func(string, ...any)) {
	where := fmt.Sprintf("apps[%d] (%s): secrets.%s", i, a.Name, name)
	if err := domain.ValidateEnvName(name); err != nil {
		bad("%s: %v", where, err)
	}
	if _, dup := a.Env[name]; dup {
		bad("%s: also set in env; a name may come from only one of them", where)
	}
	ref := a.Secrets[name]
	if (ref.Env == "") == (ref.File == "") {
		bad("%s: set exactly one of env or file", where)
	}
	if ref.Env != "" && !envVarRe.MatchString(ref.Env) {
		bad("%s: env %q is not a valid environment variable name", where, ref.Env)
	}
}
