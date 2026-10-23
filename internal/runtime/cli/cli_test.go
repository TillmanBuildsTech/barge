package cli_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/TillmanBuildsTech/barge/internal/domain"
	"github.com/TillmanBuildsTech/barge/internal/runtime"
	"github.com/TillmanBuildsTech/barge/internal/runtime/cli"
	"github.com/TillmanBuildsTech/barge/internal/transport"
	"github.com/TillmanBuildsTech/barge/internal/transport/transporttest"
)

func newDriver(t *testing.T, bin string, ex transport.Executor) runtime.Runtime {
	t.Helper()
	rt, err := cli.Factory(bin)(runtime.Deps{Exec: ex})
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

func TestFactoryRequiresExecutor(t *testing.T) {
	if _, err := cli.Factory("docker")(runtime.Deps{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
}

func TestNameIsBinary(t *testing.T) {
	for _, bin := range []string{"docker", "podman"} {
		if got := newDriver(t, bin, &transporttest.Fake{}).Name(); got != bin {
			t.Errorf("Name() = %q, want %q", got, bin)
		}
	}
}

// Argv is the contract between barge and the container CLI. These golden
// checks run for BOTH docker and podman: they must stay in lock-step.
func TestCreateArgv(t *testing.T) {
	spec := domain.ContainerSpec{
		Name:    "web",
		Image:   "nginx:1.27",
		Command: []string{"nginx", "-g", "daemon off;"},
		Env:     map[string]string{"B": "2", "A": "1"},
		Labels:  map[string]string{"z": "last", "app": "web"},
		Ports: []domain.PortMapping{
			{Host: 8080, Container: 80},
			{HostIP: "127.0.0.1", Host: 5353, Container: 53, Protocol: "udp"},
		},
		Volumes: []domain.VolumeMount{
			{Source: "data", Target: "/data"},
			{Source: "/etc/ssl", Target: "/ssl", ReadOnly: true},
		},
		Restart: domain.RestartUnlessStopped,
	}
	for _, bin := range []string{"docker", "podman"} {
		t.Run(bin, func(t *testing.T) {
			ex := &transporttest.Fake{Responses: []transporttest.Response{{Stdout: "abc123\n"}}}
			id, err := newDriver(t, bin, ex).Create(t.Context(), spec)
			if err != nil {
				t.Fatal(err)
			}
			if id != "abc123" {
				t.Errorf("id = %q", id)
			}
			want := []string{bin, "create", "--name", "web",
				"--restart", "unless-stopped",
				"--label", "app=web", "--label", "z=last",
				"-e", "A", "-e", "B",
				"-p", "8080:80/tcp", "-p", "127.0.0.1:5353:53/udp",
				"-v", "data:/data", "-v", "/etc/ssl:/ssl:ro",
				"--", "nginx:1.27", "nginx", "-g", "daemon off;"}
			got := ex.Calls[0].Argv
			if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
				t.Errorf("argv mismatch\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

func TestCreateNeverPutsEnvValuesInArgv(t *testing.T) {
	// Values on argv are visible to every user via `ps`. Only names go on
	// argv; values travel in the process environment.
	ex := &transporttest.Fake{Responses: []transporttest.Response{{Stdout: "id\n"}}}
	_, err := newDriver(t, "docker", ex).Create(t.Context(), domain.ContainerSpec{
		Name: "w", Image: "i", Env: map[string]string{"TOKEN": "hunter2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := ex.Calls[0]
	if strings.Contains(strings.Join(c.Argv, " "), "hunter2") {
		t.Fatalf("secret leaked into argv: %q", c.Argv)
	}
	if strings.Join(c.Env, ",") != "TOKEN=hunter2" {
		t.Errorf("env = %q, want TOKEN=hunter2", c.Env)
	}
}

func TestCreateRejectsInvalidSpecWithoutRunningAnything(t *testing.T) {
	ex := &transporttest.Fake{}
	_, err := newDriver(t, "docker", ex).Create(t.Context(), domain.ContainerSpec{Name: "w", Image: "--privileged"})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if len(ex.Calls) != 0 {
		t.Errorf("executor must not be called for an invalid spec: %v", ex.Calls)
	}
}

func TestSimpleVerbs(t *testing.T) {
	tests := []struct {
		name string
		call func(runtime.Runtime) error
		want []string
	}{
		{"pull", func(r runtime.Runtime) error { return r.Pull(t.Context(), "nginx:1") }, []string{"pull", "--", "nginx:1"}},
		{"start", func(r runtime.Runtime) error { return r.Start(t.Context(), "abc") }, []string{"start", "--", "abc"}},
		{"stop", func(r runtime.Runtime) error { return r.Stop(t.Context(), "abc", 15*time.Second) }, []string{"stop", "-t", "15", "--", "abc"}},
		{"remove", func(r runtime.Runtime) error { return r.Remove(t.Context(), "abc", false) }, []string{"rm", "--", "abc"}},
		{"force remove", func(r runtime.Runtime) error { return r.Remove(t.Context(), "abc", true) }, []string{"rm", "-f", "--", "abc"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex := &transporttest.Fake{}
			if err := tt.call(newDriver(t, "podman", ex)); err != nil {
				t.Fatal(err)
			}
			want := append([]string{"podman"}, tt.want...)
			if got := ex.Calls[0].Argv; strings.Join(got, "\x00") != strings.Join(want, "\x00") {
				t.Errorf("got %q want %q", got, want)
			}
		})
	}
}

func TestIDsAndRefsStartingWithDashAreRejected(t *testing.T) {
	ex := &transporttest.Fake{}
	rt := newDriver(t, "docker", ex)
	for name, err := range map[string]error{
		"pull":   rt.Pull(t.Context(), "-x"),
		"start":  rt.Start(t.Context(), "-x"),
		"stop":   rt.Stop(t.Context(), "-x", time.Second),
		"remove": rt.Remove(t.Context(), "-x", false),
	} {
		if !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
	if len(ex.Calls) != 0 {
		t.Errorf("executor was called: %v", ex.Calls)
	}
}

const dockerInspect = `[{"Id":"abc123","Name":"/web","Config":{"Image":"nginx:1.27","Labels":{"app":"web"}},"State":{"Status":"running"}}]`
const podmanInspect = `[{"Id":"def456","Name":"web","Config":{"Image":"nginx:1.27","Labels":{"app":"web"}},"State":{"Status":"exited"}}]`

func TestInspectParsesDockerAndPodmanShapes(t *testing.T) {
	tests := []struct {
		bin, out string
		id       string
		status   domain.Status
	}{
		{"docker", dockerInspect, "abc123", domain.StatusRunning},
		{"podman", podmanInspect, "def456", domain.StatusExited},
	}
	for _, tt := range tests {
		t.Run(tt.bin, func(t *testing.T) {
			ex := &transporttest.Fake{Responses: []transporttest.Response{{Stdout: tt.out}}}
			st, err := newDriver(t, tt.bin, ex).Inspect(t.Context(), "web")
			if err != nil {
				t.Fatal(err)
			}
			if st.ID != tt.id || st.Name != "web" || st.Image != "nginx:1.27" || st.Status != tt.status || st.Labels["app"] != "web" {
				t.Errorf("state = %+v", st)
			}
			if got := strings.Join(ex.Calls[0].Argv, " "); got != tt.bin+" container inspect -- web" {
				t.Errorf("argv = %q", got)
			}
		})
	}
}

func TestInspectMissingMapsToErrNotFound(t *testing.T) {
	for _, stderr := range []string{"Error: No such container: web", "Error: no such container web", "Error: no such object: web"} {
		ex := &transporttest.Fake{Responses: []transporttest.Response{{Stderr: stderr, ExitCode: 1}}}
		_, err := newDriver(t, "docker", ex).Inspect(t.Context(), "web")
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("stderr %q: want ErrNotFound, got %v", stderr, err)
		}
	}
}

func TestInspectOtherFailuresAreNotNotFound(t *testing.T) {
	ex := &transporttest.Fake{Responses: []transporttest.Response{{Stderr: "permission denied", ExitCode: 1}}}
	_, err := newDriver(t, "docker", ex).Inspect(t.Context(), "web")
	if err == nil || errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want non-NotFound error, got %v", err)
	}
}

func TestInspectGarbageOutput(t *testing.T) {
	ex := &transporttest.Fake{Responses: []transporttest.Response{{Stdout: "not json"}}}
	if _, err := newDriver(t, "docker", ex).Inspect(t.Context(), "web"); err == nil {
		t.Fatal("want parse error")
	}
}

func TestListFiltersByLabelAndInspectsInOneCall(t *testing.T) {
	ex := &transporttest.Fake{Responses: []transporttest.Response{
		{Stdout: "abc123\ndef456\n"},
		{Stdout: `[` + dockerInspect[1:len(dockerInspect)-1] + `,` + podmanInspect[1:len(podmanInspect)-1] + `]`},
	}}
	got, err := newDriver(t, "docker", ex).List(t.Context(), map[string]string{"managed-by": "barge", "app": "web"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	wantPS := "docker ps -a -q --no-trunc --filter label=app=web --filter label=managed-by=barge"
	if g := strings.Join(ex.Calls[0].Argv, " "); g != wantPS {
		t.Errorf("ps argv = %q, want %q", g, wantPS)
	}
	if g := strings.Join(ex.Calls[1].Argv, " "); g != "docker container inspect -- abc123 def456" {
		t.Errorf("inspect argv = %q", g)
	}
}

func TestListEmptySkipsInspect(t *testing.T) {
	ex := &transporttest.Fake{Responses: []transporttest.Response{{Stdout: "\n"}}}
	got, err := newDriver(t, "docker", ex).List(t.Context(), nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("got=%v err=%v", got, err)
	}
	if len(ex.Calls) != 1 {
		t.Errorf("want exactly one call, got %d", len(ex.Calls))
	}
}

func TestPing(t *testing.T) {
	ex := &transporttest.Fake{Responses: []transporttest.Response{{Stdout: "27.3.1\n"}}}
	info, err := newDriver(t, "docker", ex).Ping(t.Context())
	if err != nil || info.Version != "27.3.1" || info.Driver != "docker" {
		t.Fatalf("info=%+v err=%v", info, err)
	}
}

func TestPingUnreachableIsUnavailable(t *testing.T) {
	ex := &transporttest.Fake{Responses: []transporttest.Response{{Stderr: "Cannot connect to the Docker daemon", ExitCode: 1}}}
	_, err := newDriver(t, "docker", ex).Ping(t.Context())
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestCreateNameInUseMapsToErrConflict(t *testing.T) {
	ex := &transporttest.Fake{Responses: []transporttest.Response{{
		Stderr: `Error response from daemon: Conflict. The container name "/web" is already in use`, ExitCode: 125}}}
	_, err := newDriver(t, "docker", ex).Create(t.Context(), domain.ContainerSpec{Name: "web", Image: "nginx"})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
}

func TestVerbsOnMissingContainerMapToErrNotFound(t *testing.T) {
	calls := map[string]func(runtime.Runtime) error{
		"start":  func(r runtime.Runtime) error { return r.Start(t.Context(), "gone") },
		"stop":   func(r runtime.Runtime) error { return r.Stop(t.Context(), "gone", time.Second) },
		"remove": func(r runtime.Runtime) error { return r.Remove(t.Context(), "gone", false) },
	}
	for name, call := range calls {
		ex := &transporttest.Fake{Responses: []transporttest.Response{{Stderr: "Error: No such container: gone", ExitCode: 1}}}
		if err := call(newDriver(t, "docker", ex)); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s: want ErrNotFound, got %v", name, err)
		}
	}
}

func TestExecutorStartFailureIsPropagated(t *testing.T) {
	ex := &transporttest.Fake{Responses: []transporttest.Response{{Err: domain.ErrUnavailable}}}
	if err := newDriver(t, "docker", ex).Pull(t.Context(), "nginx"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestRenameArgvAndValidation(t *testing.T) {
	ex := &transporttest.Fake{}
	rt := newDriver(t, "podman", ex)
	if err := rt.Rename(t.Context(), "old", "new"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ex.Calls[0].Argv, " "); got != "podman rename -- old new" {
		t.Errorf("argv = %q", got)
	}
	for _, c := range [][2]string{{"-x", "new"}, {"old", "-x"}, {"old", "a b"}, {"old", ""}} {
		if err := rt.Rename(t.Context(), c[0], c[1]); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("Rename(%q,%q): want ErrInvalid, got %v", c[0], c[1], err)
		}
	}
	if len(ex.Calls) != 1 {
		t.Errorf("invalid input must not reach the executor: %v", ex.Calls)
	}
}

func TestRenameErrorMapping(t *testing.T) {
	for stderr, want := range map[string]error{
		"Error: No such container: old": domain.ErrNotFound,
		`Error response from daemon: Conflict. The container name "/new" is already in use by container "x"`: domain.ErrConflict,
	} {
		ex := &transporttest.Fake{Responses: []transporttest.Response{{Stderr: stderr, ExitCode: 1}}}
		if err := newDriver(t, "docker", ex).Rename(t.Context(), "old", "new"); !errors.Is(err, want) {
			t.Errorf("stderr %q: want %v, got %v", stderr, want, err)
		}
	}
}

func TestPodmanUniqueConstraintMapsToErrConflict(t *testing.T) {
	// Verbatim from a real podman rename onto a taken name (seen in CI).
	ex := &transporttest.Fake{Responses: []transporttest.Response{{ExitCode: 125,
		Stderr: "Error: renaming container abc: updating container config table with new configuration for container abc: UNIQUE constraint failed: ContainerConfig.Name"}}}
	if err := newDriver(t, "podman", ex).Rename(t.Context(), "a", "b"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
}

func TestPingDoesNotStutterUnavailable(t *testing.T) {
	// A missing binary already arrives wrapped as ErrUnavailable by the transport.
	ex := &transporttest.Fake{Responses: []transporttest.Response{{Err: fmt.Errorf("%w: docker not found in PATH", domain.ErrUnavailable)}}}
	_, err := newDriver(t, "docker", ex).Ping(t.Context())
	if !errors.Is(err, domain.ErrUnavailable) || strings.Count(err.Error(), "unavailable") != 1 {
		t.Errorf("want exactly one 'unavailable', got %q", err)
	}
}
