//go:build integration

package cli_test

import (
	"os/exec"
	"testing"

	"github.com/TillmanBuildsTech/barge/internal/runtime"
	"github.com/TillmanBuildsTech/barge/internal/runtime/cli"
	"github.com/TillmanBuildsTech/barge/internal/runtime/runtimetest"
	"github.com/TillmanBuildsTech/barge/internal/transport"
)

// TestRealRuntimesSatisfyContract runs the shared contract against every
// installed container CLI. A runtime that is not installed is skipped, not
// failed, so the same command works on any dev machine.
func TestRealRuntimesSatisfyContract(t *testing.T) {
	for _, bin := range []string{"docker", "podman"} {
		t.Run(bin, func(t *testing.T) {
			if _, err := exec.LookPath(bin); err != nil {
				t.Skipf("%s not installed", bin)
			}
			runtimetest.Run(t, func(t *testing.T) runtime.Runtime {
				rt, err := cli.Factory(bin)(runtime.Deps{Exec: transport.Local{}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := rt.Ping(t.Context()); err != nil {
					t.Skipf("%s daemon not reachable: %v", bin, err)
				}
				return rt
			})
		})
	}
}
