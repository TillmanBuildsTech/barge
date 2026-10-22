package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/TillmanBuildsTech/barge/internal/domain"
)

func validSpec() domain.ContainerSpec {
	return domain.ContainerSpec{Name: "web", Image: "nginx:1.27"}
}

func TestContainerSpecValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*domain.ContainerSpec)
		wantErr string // substring; empty means valid
	}{
		{"minimal is valid", func(*domain.ContainerSpec) {}, ""},
		{"empty name", func(s *domain.ContainerSpec) { s.Name = "" }, "name"},
		{"name with space", func(s *domain.ContainerSpec) { s.Name = "my app" }, "name"},
		{"name starting with dash is a flag", func(s *domain.ContainerSpec) { s.Name = "-rm" }, "name"},
		{"name with shell metachar", func(s *domain.ContainerSpec) { s.Name = "a;b" }, "name"},
		{"empty image", func(s *domain.ContainerSpec) { s.Image = "" }, "image"},
		{"image starting with dash is a flag", func(s *domain.ContainerSpec) { s.Image = "--privileged" }, "image"},
		{"image with whitespace", func(s *domain.ContainerSpec) { s.Image = "nginx latest" }, "image"},
		{"registry with port and digest", func(s *domain.ContainerSpec) {
			s.Image = "registry.example.com:5000/team/app@sha256:" + strings.Repeat("a", 64)
		}, ""},
		{"port zero", func(s *domain.ContainerSpec) {
			s.Ports = []domain.PortMapping{{Host: 0, Container: 80}}
		}, "ports[0]"},
		{"port too high", func(s *domain.ContainerSpec) {
			s.Ports = []domain.PortMapping{{Host: 8080, Container: 70000}}
		}, "ports[0]"},
		{"bad protocol", func(s *domain.ContainerSpec) {
			s.Ports = []domain.PortMapping{{Host: 80, Container: 80, Protocol: "icmp"}}
		}, "protocol"},
		{"good port", func(s *domain.ContainerSpec) {
			s.Ports = []domain.PortMapping{{Host: 8080, Container: 80, Protocol: "tcp"}}
		}, ""},
		{"relative volume target", func(s *domain.ContainerSpec) {
			s.Volumes = []domain.VolumeMount{{Source: "data", Target: "var/data"}}
		}, "volumes[0]"},
		{"empty volume source", func(s *domain.ContainerSpec) {
			s.Volumes = []domain.VolumeMount{{Source: "", Target: "/data"}}
		}, "volumes[0]"},
		{"unknown restart policy", func(s *domain.ContainerSpec) { s.Restart = "sometimes" }, "restart"},
		{"known restart policy", func(s *domain.ContainerSpec) { s.Restart = domain.RestartUnlessStopped }, ""},
		{"env key with equals", func(s *domain.ContainerSpec) { s.Env = map[string]string{"A=B": "x"} }, "env"},
		{"empty env key", func(s *domain.ContainerSpec) { s.Env = map[string]string{"": "x"} }, "env"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validSpec()
			tt.mutate(&s)
			err := s.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tt.wantErr)
			}
			if !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("error %v does not wrap ErrInvalid", err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestContainerSpecValidateReportsAllProblems(t *testing.T) {
	s := domain.ContainerSpec{Name: "", Image: ""}
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), "name") || !strings.Contains(err.Error(), "image") {
		t.Fatalf("want both name and image reported, got %v", err)
	}
}

func FuzzValidateNeverPanics(f *testing.F) {
	f.Add("web", "nginx:1")
	f.Add("-x", "--y")
	f.Fuzz(func(_ *testing.T, name, image string) {
		_ = domain.ContainerSpec{Name: name, Image: image}.Validate()
	})
}
