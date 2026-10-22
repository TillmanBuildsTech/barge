//go:build integration

package deploy_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os/exec"
	"testing"
	"time"

	"github.com/TillmanBuildsTech/barge/internal/deploy"
	"github.com/TillmanBuildsTech/barge/internal/domain"
	"github.com/TillmanBuildsTech/barge/internal/runtime"
	"github.com/TillmanBuildsTech/barge/internal/runtime/cli"
	"github.com/TillmanBuildsTech/barge/internal/runtime/runtimetest"
	"github.com/TillmanBuildsTech/barge/internal/transport"
)

// TestRealRuntimeDeployLifecycle drives the engine against every installed
// container CLI: create, idempotent re-apply, replace, a replacement that
// crashes (must roll back to the very same container), and a replacement whose
// image cannot be pulled.
func TestRealRuntimeDeployLifecycle(t *testing.T) {
	for _, bin := range []string{"docker", "podman"} {
		t.Run(bin, func(t *testing.T) {
			if _, err := exec.LookPath(bin); err != nil {
				t.Skipf("%s not installed", bin)
			}
			rt, err := cli.Factory(bin)(runtime.Deps{Exec: transport.Local{}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := rt.Ping(t.Context()); err != nil {
				t.Skipf("%s daemon not reachable: %v", bin, err)
			}
			if err := rt.Pull(t.Context(), runtimetest.Image); err != nil {
				t.Fatal(err)
			}
			lifecycle(t, rt)
		})
	}
}

func lifecycle(t *testing.T, rt runtime.Runtime) {
	var b [4]byte
	_, _ = rand.Read(b[:])
	name := "barge-it-" + hex.EncodeToString(b[:])
	t.Cleanup(func() {
		bg := context.WithoutCancel(t.Context())
		for _, n := range []string{name, name + "-barge-new", name + "-barge-old"} {
			_ = rt.Remove(bg, n, true)
		}
	})

	e := deploy.New(rt, deploy.Options{Settle: 2 * time.Second, PollInterval: 200 * time.Millisecond, StopTimeout: 2 * time.Second})
	mk := func(cmd ...string) domain.ContainerSpec {
		return domain.ContainerSpec{
			Name: name, Image: runtimetest.Image, Command: cmd,
			// The container only stays up if the env value really arrives:
			// this validates env-by-process-environment on a real runtime.
			Env: map[string]string{"BARGE_IT": "expected value with spaces & $symbols"},
		}
	}
	good := func(n string) domain.ContainerSpec {
		return mk("sh", "-c", `test "$BARGE_IT" = 'expected value with spaces & $symbols' && sleep `+n)
	}
	apply := func(s domain.ContainerSpec) deploy.Result {
		t.Helper()
		plan, err := e.Plan(t.Context(), []domain.ContainerSpec{s})
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		res, err := e.Apply(t.Context(), plan)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		return res[0]
	}
	current := func() domain.ContainerState {
		t.Helper()
		st, err := rt.Inspect(t.Context(), name)
		if err != nil {
			t.Fatalf("Inspect %s: %v", name, err)
		}
		return st
	}

	// 1. create
	if r := apply(good("300")); r.Outcome != deploy.OutcomeApplied || r.Err != nil {
		t.Fatalf("create: %+v", r)
	}
	first := current()
	if first.Status != domain.StatusRunning || first.Labels[domain.LabelSpecHash] != good("300").Hash() {
		t.Fatalf("after create: %+v", first)
	}

	// 2. idempotent
	if r := apply(good("300")); r.Outcome != deploy.OutcomeUnchanged {
		t.Fatalf("re-apply: %+v", r)
	}
	if current().ID != first.ID {
		t.Fatal("re-apply recreated the container")
	}

	// 3. replace
	if r := apply(good("301")); r.Outcome != deploy.OutcomeApplied || r.Action != deploy.ActionReplace || r.Err != nil {
		t.Fatalf("replace: %+v", r)
	}
	second := current()
	if second.ID == first.ID || second.Status != domain.StatusRunning {
		t.Fatalf("replace did not produce a new running container: %+v", second)
	}
	for _, leftover := range []string{name + "-barge-new", name + "-barge-old"} {
		if _, err := rt.Inspect(t.Context(), leftover); err == nil {
			t.Errorf("leftover container %s after a successful replace", leftover)
		}
	}

	// 4. replacement that exits immediately: must roll back to the SAME container
	r := apply(mk("sh", "-c", "exit 1"))
	if r.Outcome != deploy.OutcomeFailed || !r.RolledBack || r.RollbackErr != nil {
		t.Fatalf("crashing replace: %+v", r)
	}
	after := current()
	if after.ID != second.ID || after.Status != domain.StatusRunning {
		t.Fatalf("rollback did not restore the previous container: before=%+v after=%+v", second, after)
	}
	if _, err := rt.Inspect(t.Context(), name+"-barge-new"); err == nil {
		t.Error("staging container left behind after rollback")
	}

	// 5. replacement whose image cannot be pulled: the old one is never touched
	bad := good("302")
	bad.Image = "docker.io/library/barge-does-not-exist-" + name + ":1"
	r = apply(bad)
	if r.Outcome != deploy.OutcomeFailed || r.Err == nil {
		t.Fatalf("unpullable image: %+v", r)
	}
	if got := current(); got.ID != second.ID || got.Status != domain.StatusRunning {
		t.Fatalf("a failed pull must leave the running container alone: %+v", got)
	}

	// 6. an unmanaged container of the same name blocks the plan
	other := name + "-other"
	t.Cleanup(func() { _ = rt.Remove(context.WithoutCancel(t.Context()), other, true) })
	if _, err := rt.Create(t.Context(), domain.ContainerSpec{Name: other, Image: runtimetest.Image, Command: []string{"sleep", "300"}}); err != nil {
		t.Fatal(err)
	}
	s := good("300")
	s.Name = other
	plan, err := e.Plan(t.Context(), []domain.ContainerSpec{s})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Items[0].Action != deploy.ActionBlocked {
		t.Errorf("an unmanaged container must block: %+v", plan.Items[0])
	}
}
