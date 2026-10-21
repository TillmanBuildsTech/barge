// Package runtimetest is the behavioural contract every runtime.Runtime driver
// must satisfy. Run it against the fake in unit tests and against real
// docker/podman in integration tests; if both pass, drivers are
// interchangeable.
package runtimetest

import (
	"errors"
	"testing"
	"time"

	"github.com/TillmanBuildsTech/barge/internal/domain"
	"github.com/TillmanBuildsTech/barge/internal/runtime"
)

// Image is a tiny image real-runtime integration runs should have pulled.
const Image = "docker.io/library/alpine:3.20"

// Run executes the contract. newRT must return a fresh, empty runtime.
// Containers should be cleaned up by the driver's backing environment.
func Run(t *testing.T, newRT func(t *testing.T) runtime.Runtime) {
	t.Helper()

	spec := func(name string) domain.ContainerSpec {
		return domain.ContainerSpec{
			Name: name, Image: Image, Command: []string{"sleep", "300"},
			Labels: map[string]string{"managed-by": "barge-test", "name": name},
		}
	}
	setup := func(t *testing.T) runtime.Runtime {
		t.Helper()
		rt := newRT(t)
		if err := rt.Pull(t.Context(), Image); err != nil {
			t.Fatalf("Pull: %v", err)
		}
		return rt
	}

	t.Run("Ping", func(t *testing.T) {
		info, err := newRT(t).Ping(t.Context())
		if err != nil || info.Driver == "" {
			t.Fatalf("Ping = %+v, %v", info, err)
		}
	})

	t.Run("lifecycle", func(t *testing.T) {
		rt := setup(t)
		ctx := t.Context()
		id, err := rt.Create(ctx, spec("c-life"))
		if err != nil || id == "" {
			t.Fatalf("Create = %q, %v", id, err)
		}
		t.Cleanup(func() { _ = rt.Remove(t.Context(), "c-life", true) })

		st, err := rt.Inspect(ctx, "c-life")
		if err != nil {
			t.Fatal(err)
		}
		if st.Name != "c-life" || st.Status != domain.StatusCreated || st.Labels["name"] != "c-life" {
			t.Errorf("after create: %+v", st)
		}
		if byID, err := rt.Inspect(ctx, id); err != nil || byID.Name != "c-life" {
			t.Errorf("inspect by ID: %+v, %v", byID, err)
		}

		if err := rt.Start(ctx, "c-life"); err != nil {
			t.Fatal(err)
		}
		if err := rt.Start(ctx, "c-life"); err != nil {
			t.Errorf("Start must be idempotent: %v", err)
		}
		if st, _ := rt.Inspect(ctx, "c-life"); st.Status != domain.StatusRunning {
			t.Errorf("want running, got %v", st.Status)
		}

		if err := rt.Remove(ctx, "c-life", false); err == nil {
			t.Error("removing a running container without force must fail")
		}

		if err := rt.Stop(ctx, "c-life", 2*time.Second); err != nil {
			t.Fatal(err)
		}
		if err := rt.Stop(ctx, "c-life", 2*time.Second); err != nil {
			t.Errorf("Stop must be idempotent: %v", err)
		}
		if st, _ := rt.Inspect(ctx, "c-life"); st.Status != domain.StatusExited {
			t.Errorf("want exited, got %v", st.Status)
		}

		if err := rt.Remove(ctx, "c-life", false); err != nil {
			t.Fatal(err)
		}
		if _, err := rt.Inspect(ctx, "c-life"); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("after remove: want ErrNotFound, got %v", err)
		}
	})

	t.Run("duplicate name is ErrConflict", func(t *testing.T) {
		rt := setup(t)
		if _, err := rt.Create(t.Context(), spec("c-dup")); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = rt.Remove(t.Context(), "c-dup", true) })
		if _, err := rt.Create(t.Context(), spec("c-dup")); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("want ErrConflict, got %v", err)
		}
	})

	t.Run("invalid spec is ErrInvalid", func(t *testing.T) {
		rt := newRT(t)
		bad := spec("ok")
		bad.Image = "--privileged"
		if _, err := rt.Create(t.Context(), bad); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("want ErrInvalid, got %v", err)
		}
	})

	t.Run("missing container is ErrNotFound", func(t *testing.T) {
		rt := newRT(t)
		ctx := t.Context()
		for name, err := range map[string]error{
			"Inspect": func() error { _, e := rt.Inspect(ctx, "c-missing"); return e }(),
			"Start":   rt.Start(ctx, "c-missing"),
			"Stop":    rt.Stop(ctx, "c-missing", time.Second),
			"Remove":  rt.Remove(ctx, "c-missing", true),
		} {
			if !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("%s: want ErrNotFound, got %v", name, err)
			}
		}
	})

	t.Run("List filters by every label", func(t *testing.T) {
		rt := setup(t)
		ctx := t.Context()
		for _, n := range []string{"c-list-a", "c-list-b"} {
			if _, err := rt.Create(ctx, spec(n)); err != nil {
				t.Fatal(err)
			}
			n := n
			t.Cleanup(func() { _ = rt.Remove(t.Context(), n, true) })
		}
		all, err := rt.List(ctx, map[string]string{"managed-by": "barge-test"})
		if err != nil || len(all) < 2 {
			t.Fatalf("List(all) = %d, %v", len(all), err)
		}
		one, err := rt.List(ctx, map[string]string{"managed-by": "barge-test", "name": "c-list-b"})
		if err != nil || len(one) != 1 || one[0].Name != "c-list-b" {
			t.Fatalf("List(one) = %+v, %v", one, err)
		}
		none, err := rt.List(ctx, map[string]string{"name": "does-not-exist"})
		if err != nil || len(none) != 0 {
			t.Fatalf("List(none) = %+v, %v", none, err)
		}
	})
}
