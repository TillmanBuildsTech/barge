// Package cli implements runtime.Runtime by driving a docker-compatible
// container CLI (docker, podman) through a transport.Executor. Docker and
// Podman share one implementation parameterised by binary name; where they
// diverge, the divergence is handled here and pinned by tests.
//
// Because commands go through an Executor, the same driver works against the
// local machine or a remote host.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/TillmanBuildsTech/barge/internal/domain"
	"github.com/TillmanBuildsTech/barge/internal/runtime"
	"github.com/TillmanBuildsTech/barge/internal/transport"
)

// Driver drives one docker-compatible CLI.
type Driver struct {
	bin  string
	exec transport.Executor
}

var _ runtime.Runtime = (*Driver)(nil)

// Factory returns a runtime.Factory for the named CLI binary, e.g.
// cli.Factory("docker") or cli.Factory("podman").
func Factory(binary string) runtime.Factory {
	return func(d runtime.Deps) (runtime.Runtime, error) {
		if d.Exec == nil {
			return nil, fmt.Errorf("%w: %s driver needs an executor", domain.ErrInvalid, binary)
		}
		return &Driver{bin: binary, exec: d.Exec}, nil
	}
}

// Name implements runtime.Runtime.
func (d *Driver) Name() string { return d.bin }

// run executes `<bin> args...` and translates well-known failures into
// domain sentinels so callers never parse CLI text.
func (d *Driver) run(ctx context.Context, env []string, args ...string) (transport.Result, error) {
	argv := append([]string{d.bin}, args...)
	res, err := d.exec.Run(ctx, transport.Cmd{Argv: argv, Env: env})
	if err == nil {
		return res, nil
	}
	verb := args[0]
	var ee *transport.ExitError
	if errors.As(err, &ee) {
		low := strings.ToLower(ee.Stderr)
		switch {
		case strings.Contains(low, "no such "):
			return res, fmt.Errorf("%s %s: %w: %w", d.bin, verb, domain.ErrNotFound, err)
		case strings.Contains(low, "already in use"):
			return res, fmt.Errorf("%s %s: %w: %w", d.bin, verb, domain.ErrConflict, err)
		}
	}
	return res, fmt.Errorf("%s %s: %w", d.bin, verb, err)
}

// checkArg rejects values that could be parsed as flags or that cannot be a
// single CLI token. Positional arguments are additionally preceded by "--".
func checkArg(kind, v string) error {
	if v == "" || strings.HasPrefix(v, "-") || strings.ContainsAny(v, " \t\r\n\x00") {
		return fmt.Errorf("%w: %s %q is not a valid identifier", domain.ErrInvalid, kind, v)
	}
	return nil
}

// Ping implements runtime.Runtime.
func (d *Driver) Ping(ctx context.Context) (runtime.Info, error) {
	res, err := d.run(ctx, nil, "version", "--format", "{{.Server.Version}}")
	if err != nil {
		return runtime.Info{}, fmt.Errorf("%w: %w", domain.ErrUnavailable, err)
	}
	return runtime.Info{Driver: d.bin, Version: strings.TrimSpace(string(res.Stdout))}, nil
}

// Pull implements runtime.Runtime.
func (d *Driver) Pull(ctx context.Context, ref string) error {
	if err := checkArg("image", ref); err != nil {
		return err
	}
	_, err := d.run(ctx, nil, "pull", "--", ref)
	return err
}

// Create implements runtime.Runtime.
func (d *Driver) Create(ctx context.Context, s domain.ContainerSpec) (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	args := []string{"create", "--name", s.Name}
	if s.Restart != "" {
		args = append(args, "--restart", string(s.Restart))
	}
	for _, k := range sortedKeys(s.Labels) {
		args = append(args, "--label", k+"="+s.Labels[k])
	}
	// Only variable NAMES go on argv (visible via ps); values travel in the
	// process environment, which `-e NAME` makes the CLI forward.
	var env []string
	for _, k := range sortedKeys(s.Env) {
		args = append(args, "-e", k)
		env = append(env, k+"="+s.Env[k])
	}
	for _, p := range s.Ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		m := strconv.Itoa(p.Host) + ":" + strconv.Itoa(p.Container) + "/" + proto
		if p.HostIP != "" {
			m = p.HostIP + ":" + m
		}
		args = append(args, "-p", m)
	}
	for _, v := range s.Volumes {
		m := v.Source + ":" + v.Target
		if v.ReadOnly {
			m += ":ro"
		}
		args = append(args, "-v", m)
	}
	args = append(args, "--", s.Image)
	args = append(args, s.Command...)

	res, err := d.run(ctx, env, args...)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(res.Stdout))
	if id == "" {
		return "", fmt.Errorf("%s create: runtime returned no container ID", d.bin)
	}
	return id, nil
}

// Start implements runtime.Runtime.
func (d *Driver) Start(ctx context.Context, id string) error {
	if err := checkArg("container", id); err != nil {
		return err
	}
	_, err := d.run(ctx, nil, "start", "--", id)
	return err
}

// Stop implements runtime.Runtime.
func (d *Driver) Stop(ctx context.Context, id string, timeout time.Duration) error {
	if err := checkArg("container", id); err != nil {
		return err
	}
	_, err := d.run(ctx, nil, "stop", "-t", strconv.Itoa(int(timeout.Seconds())), "--", id)
	return err
}

// Remove implements runtime.Runtime.
func (d *Driver) Remove(ctx context.Context, id string, force bool) error {
	if err := checkArg("container", id); err != nil {
		return err
	}
	args := []string{"rm"}
	if force {
		args = append(args, "-f")
	}
	_, err := d.run(ctx, nil, append(args, "--", id)...)
	return err
}

// Inspect implements runtime.Runtime.
func (d *Driver) Inspect(ctx context.Context, id string) (domain.ContainerState, error) {
	if err := checkArg("container", id); err != nil {
		return domain.ContainerState{}, err
	}
	states, err := d.inspect(ctx, id)
	if err != nil {
		return domain.ContainerState{}, err
	}
	if len(states) != 1 {
		return domain.ContainerState{}, fmt.Errorf("%s inspect %q: expected 1 result, got %d", d.bin, id, len(states))
	}
	return states[0], nil
}

// List implements runtime.Runtime. It resolves IDs with `ps` and fetches full
// state with one `inspect`, which has an identical JSON shape on docker and
// podman (unlike `ps --format json`).
func (d *Driver) List(ctx context.Context, labels map[string]string) ([]domain.ContainerState, error) {
	args := []string{"ps", "-a", "-q", "--no-trunc"}
	for _, k := range sortedKeys(labels) {
		args = append(args, "--filter", "label="+k+"="+labels[k])
	}
	res, err := d.run(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(res.Stdout))
	if len(ids) == 0 {
		return nil, nil
	}
	return d.inspect(ctx, ids...)
}

func (d *Driver) inspect(ctx context.Context, ids ...string) ([]domain.ContainerState, error) {
	args := append([]string{"container", "inspect", "--"}, ids...)
	res, err := d.run(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID     string `json:"Id"`
		Name   string `json:"Name"`
		Config struct {
			Image  string            `json:"Image"`
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		State struct {
			Status string `json:"Status"`
		} `json:"State"`
	}
	if err := json.Unmarshal(res.Stdout, &raw); err != nil {
		return nil, fmt.Errorf("%s inspect: parse output: %w", d.bin, err)
	}
	out := make([]domain.ContainerState, 0, len(raw))
	for _, r := range raw {
		out = append(out, domain.ContainerState{
			ID:     r.ID,
			Name:   strings.TrimPrefix(r.Name, "/"), // docker prefixes "/", podman does not
			Image:  r.Config.Image,
			Status: mapStatus(r.State.Status),
			Labels: r.Config.Labels,
		})
	}
	return out, nil
}

func mapStatus(s string) domain.Status {
	switch strings.ToLower(s) {
	case "created", "configured":
		return domain.StatusCreated
	case "running":
		return domain.StatusRunning
	case "exited", "stopped", "dead":
		return domain.StatusExited
	default:
		return domain.StatusUnknown
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
