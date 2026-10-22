package domain

import (
	"fmt"
	"regexp"
	"strings"
)

// RestartPolicy is a container restart policy. The empty value means "use the
// runtime's default".
type RestartPolicy string

// Supported restart policies. They are spelled identically by docker and podman.
const (
	RestartNo            RestartPolicy = "no"
	RestartAlways        RestartPolicy = "always"
	RestartOnFailure     RestartPolicy = "on-failure"
	RestartUnlessStopped RestartPolicy = "unless-stopped"
)

// PortMapping publishes a container port on the host.
type PortMapping struct {
	HostIP    string // optional bind address
	Host      int
	Container int
	Protocol  string // "tcp" (default) or "udp"
}

// VolumeMount mounts a host path or named volume into the container.
type VolumeMount struct {
	Source   string
	Target   string // absolute path inside the container
	ReadOnly bool
}

// ContainerSpec is the runtime-agnostic description of a container. Runtime
// drivers translate it; they never receive raw shell strings.
type ContainerSpec struct {
	Name    string
	Image   string
	Command []string
	Env     map[string]string
	Labels  map[string]string
	Ports   []PortMapping
	Volumes []VolumeMount
	Restart RestartPolicy
}

var (
	// Names and image refs may never start with '-' (they would be parsed as
	// flags by the container CLI) or contain whitespace/shell metacharacters.
	nameRe  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
	imageRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/@-]*$`)
)

// ValidateName checks a container name. The error wraps ErrInvalid.
func ValidateName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("%w: name %q must match %s", ErrInvalid, name, nameRe)
	}
	return nil
}

// Validate reports every problem with the spec at once. The returned error
// wraps ErrInvalid.
func (s ContainerSpec) Validate() error {
	var p []string
	if !nameRe.MatchString(s.Name) {
		p = append(p, fmt.Sprintf("name %q must match %s", s.Name, nameRe))
	}
	if !imageRe.MatchString(s.Image) {
		p = append(p, fmt.Sprintf("image %q is not a valid image reference", s.Image))
	}
	for i, pm := range s.Ports {
		if pm.Host < 1 || pm.Host > 65535 || pm.Container < 1 || pm.Container > 65535 {
			p = append(p, fmt.Sprintf("ports[%d]: host and container must be 1-65535", i))
		}
		switch pm.Protocol {
		case "", "tcp", "udp":
		default:
			p = append(p, fmt.Sprintf("ports[%d]: protocol %q must be tcp or udp", i, pm.Protocol))
		}
	}
	for i, v := range s.Volumes {
		if v.Source == "" || !strings.HasPrefix(v.Target, "/") {
			p = append(p, fmt.Sprintf("volumes[%d]: source is required and target must be an absolute path", i))
		}
	}
	switch s.Restart {
	case "", RestartNo, RestartAlways, RestartOnFailure, RestartUnlessStopped:
	default:
		p = append(p, fmt.Sprintf("restart policy %q is not supported", s.Restart))
	}
	for k := range s.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") {
			p = append(p, fmt.Sprintf("env key %q is invalid", k))
		}
	}
	if len(p) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrInvalid, strings.Join(p, "; "))
}

// Status is the coarse lifecycle state of a container.
type Status string

// Container statuses.
const (
	StatusCreated Status = "created"
	StatusRunning Status = "running"
	StatusExited  Status = "exited"
	StatusUnknown Status = "unknown"
)

// ContainerState is what a runtime reports about an existing container.
type ContainerState struct {
	ID     string
	Name   string
	Image  string
	Status Status
	Labels map[string]string
}
