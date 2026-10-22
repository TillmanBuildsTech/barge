package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/TillmanBuildsTech/barge/internal/domain"
)

func TestValidateEnvName(t *testing.T) {
	for _, ok := range []string{"A", "LOG_LEVEL", "_x", "db2_password", "Mixed_Case1"} {
		if err := domain.ValidateEnvName(ok); err != nil {
			t.Errorf("ValidateEnvName(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "1A", "A B", "A=B", "A-B", "A;rm", "A\x00", "ünï", "A\nB"} {
		if err := domain.ValidateEnvName(bad); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("ValidateEnvName(%q) should fail with ErrInvalid, got %v", bad, err)
		}
	}
}

// barge hands env values to the container CLI through the CLI's own process
// environment (so they never appear on a command line). A name that changes
// how that CLI behaves would therefore be an injection, so such names are
// refused everywhere.
func TestValidateEnvNameRefusesNamesThatSteerTheContainerCLI(t *testing.T) {
	for _, name := range []string{
		"PATH", "path", "HOME", "USER", "SHELL", "TMPDIR", "LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES",
		"DOCKER_HOST", "DOCKER_CONFIG", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "CONTAINER_HOST", "PODMAN_HOST",
		"XDG_RUNTIME_DIR", "SSH_AUTH_SOCK", "HTTP_PROXY", "https_proxy", "NO_PROXY", "SYSTEMROOT",
	} {
		err := domain.ValidateEnvName(name)
		if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("%q should be rejected as reserved, got %v", name, err)
		}
	}
	for _, ok := range []string{"DOCKERFILE_PATH", "MY_HOME", "PATHS", "APP_PATH", "LDAP_URL", "USERNAME"} {
		if err := domain.ValidateEnvName(ok); err != nil {
			t.Errorf("%q must remain allowed: %v", ok, err)
		}
	}
}

func TestSpecValidateAppliesEnvNameRules(t *testing.T) {
	s := validSpec()
	s.Env = map[string]string{"DOCKER_HOST": "tcp://evil:2375"}
	if err := s.Validate(); !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "DOCKER_HOST") {
		t.Fatalf("want ErrInvalid naming DOCKER_HOST, got %v", err)
	}
	s.Env = map[string]string{"1BAD": "x"}
	if err := s.Validate(); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
}
