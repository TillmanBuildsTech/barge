package domain

import (
	"fmt"
	"regexp"
	"strings"
)

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Barge delivers env values to the container CLI through that CLI's own
// process environment (`-e NAME` plus NAME=value in the environment), so that
// values never appear on a command line where `ps` could show them. The cost
// is that a container variable shares a namespace with the variables that
// steer the CLI itself: setting DOCKER_HOST or PATH for a container would
// silently redirect or hijack the CLI. Those names are therefore refused.
var (
	reservedEnvExact = map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
		"TMPDIR": true, "TMP": true, "TEMP": true,
		"USERPROFILE": true, "APPDATA": true, "LOCALAPPDATA": true,
		"SYSTEMROOT": true, "WINDIR": true, "COMSPEC": true, "PATHEXT": true,
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true, "ALL_PROXY": true,
	}
	reservedEnvPrefix = []string{"DOCKER_", "CONTAINER_", "PODMAN_", "BUILDAH_", "XDG_", "LD_", "DYLD_", "SSH_"}
)

// envNameProblem returns "" for an acceptable container env var name.
func envNameProblem(name string) string {
	if !envNameRe.MatchString(name) {
		return fmt.Sprintf("env name %q must match %s", name, envNameRe)
	}
	up := strings.ToUpper(name)
	reserved := reservedEnvExact[up]
	for _, p := range reservedEnvPrefix {
		reserved = reserved || strings.HasPrefix(up, p)
	}
	if reserved {
		return fmt.Sprintf("env name %q is reserved: it would change how the container CLI itself behaves", name)
	}
	return ""
}

// ValidateEnvName checks a container environment variable name. The error
// wraps ErrInvalid.
func ValidateEnvName(name string) error {
	if p := envNameProblem(name); p != "" {
		return fmt.Errorf("%w: %s", ErrInvalid, p)
	}
	return nil
}
