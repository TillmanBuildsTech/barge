package domain_test

import (
	"regexp"
	"testing"

	"github.com/TillmanBuildsTech/barge/internal/domain"
)

func base() domain.ContainerSpec {
	return domain.ContainerSpec{
		Name: "web", Image: "nginx:1.27", Command: []string{"nginx"},
		Env:     map[string]string{"A": "1"},
		Labels:  map[string]string{"team": "x"},
		Ports:   []domain.PortMapping{{Host: 8080, Container: 80}},
		Volumes: []domain.VolumeMount{{Source: "data", Target: "/data"}},
		Restart: domain.RestartAlways,
	}
}

func TestHashFormatAndStability(t *testing.T) {
	h := base().Hash()
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(h) {
		t.Fatalf("hash %q is not 32 hex chars", h)
	}
	if h != base().Hash() {
		t.Error("hash must be deterministic")
	}
}

func TestHashTreatsNilAndEmptyAlike(t *testing.T) {
	a := domain.ContainerSpec{Name: "w", Image: "i"}
	b := domain.ContainerSpec{
		Name: "w", Image: "i",
		Command: []string{}, Env: map[string]string{}, Labels: map[string]string{},
		Ports: []domain.PortMapping{}, Volumes: []domain.VolumeMount{},
	}
	if a.Hash() != b.Hash() {
		t.Error("nil and empty collections must hash identically (config decoding yields either)")
	}
}

func TestHashChangesWithEverythingThatMatters(t *testing.T) {
	mutations := map[string]func(*domain.ContainerSpec){
		"name":        func(s *domain.ContainerSpec) { s.Name = "other" },
		"image":       func(s *domain.ContainerSpec) { s.Image = "nginx:1.28" },
		"command":     func(s *domain.ContainerSpec) { s.Command = []string{"nginx", "-t"} },
		"env value":   func(s *domain.ContainerSpec) { s.Env = map[string]string{"A": "2"} },
		"env added":   func(s *domain.ContainerSpec) { s.Env = map[string]string{"A": "1", "B": "2"} },
		"label":       func(s *domain.ContainerSpec) { s.Labels = map[string]string{"team": "y"} },
		"port":        func(s *domain.ContainerSpec) { s.Ports = []domain.PortMapping{{Host: 8081, Container: 80}} },
		"port proto":  func(s *domain.ContainerSpec) { s.Ports[0].Protocol = "udp" },
		"volume":      func(s *domain.ContainerSpec) { s.Volumes[0].ReadOnly = true },
		"restart":     func(s *domain.ContainerSpec) { s.Restart = domain.RestartNo },
		"command arg": func(s *domain.ContainerSpec) { s.Command = []string{"nginx"}; s.Command[0] = "nginx2" },
	}
	want := base().Hash()
	for name, mut := range mutations {
		s := base()
		mut(&s)
		if s.Hash() == want {
			t.Errorf("changing %s did not change the hash", name)
		}
	}
}

func TestHashFieldBoundariesAreUnambiguous(t *testing.T) {
	a := domain.ContainerSpec{Name: "w", Image: "i", Command: []string{"a b"}}
	b := domain.ContainerSpec{Name: "w", Image: "i", Command: []string{"a", "b"}}
	if a.Hash() == b.Hash() {
		t.Error(`["a b"] and ["a","b"] must not collide`)
	}
}

func TestReservedLabels(t *testing.T) {
	for k, want := range map[string]bool{
		"managed-by": true, "barge.app": true, "barge.anything": true,
		"team": false, "barge": false, "my.barge.app": false,
	} {
		if got := domain.ReservedLabel(k); got != want {
			t.Errorf("ReservedLabel(%q) = %v, want %v", k, got, want)
		}
	}
}

func TestValidateName(t *testing.T) {
	for _, ok := range []string{"web", "web-barge-new", "a.b_c-1"} {
		if err := domain.ValidateName(ok); err != nil {
			t.Errorf("ValidateName(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-x", "a b", "a;b", "/x", "a/b"} {
		if err := domain.ValidateName(bad); err == nil {
			t.Errorf("ValidateName(%q) should fail", bad)
		}
	}
}
