// Package app is barge's command layer. It is deliberately independent of
// os.Args/os.Exit so every command is unit-testable; cmd/barge only wires it
// to the process.
package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/TillmanBuildsTech/barge/internal/config"
	"github.com/TillmanBuildsTech/barge/internal/runtime"
	"github.com/TillmanBuildsTech/barge/internal/runtime/cli"
)

// Exit codes.
const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
)

// DefaultRegistry registers the runtime drivers compiled into this binary.
// Adding a runtime means adding one line here.
func DefaultRegistry() *runtime.Registry {
	r := runtime.NewRegistry()
	for _, bin := range []string{"docker", "podman"} {
		if err := r.Register(bin, cli.Factory(bin)); err != nil {
			panic(err) // programmer error: duplicate built-in name
		}
	}
	return r
}

// App holds the dependencies commands need.
type App struct {
	version  string
	registry *runtime.Registry
}

// New builds an App.
func New(version string, reg *runtime.Registry) *App {
	return &App{version: version, registry: reg}
}

const usage = "usage: barge <command>\n\ncommands:\n  version            print the version\n  drivers            list available runtime drivers\n  validate -config F validate a config file\n"

// Run executes one command and returns the process exit code.
func (a *App) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return ExitUsage
	}
	switch args[0] {
	case "version":
		_, _ = fmt.Fprintf(stdout, "barge %s\n", a.version)
		return ExitOK
	case "drivers":
		_, _ = fmt.Fprintln(stdout, strings.Join(a.registry.Names(), "\n"))
		return ExitOK
	case "validate":
		return a.validate(ctx, args[1:], stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command %q\n%s", args[0], usage)
		return ExitUsage
	}
}

func (a *App) validate(_ context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "", "path to the barge config file")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *path == "" {
		_, _ = fmt.Fprintln(stderr, "validate: -config is required")
		return ExitUsage
	}

	cfg, err := config.LoadFile(*path)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitFailure
	}
	// Fail at startup, not mid-deploy: every driver named in the config must
	// exist in this binary.
	drivers := map[string]string{"runtime.driver": cfg.Runtime.Driver}
	for i, t := range cfg.Targets {
		drivers[fmt.Sprintf("targets[%d].runtime.driver", i)] = t.Runtime.Driver
	}
	known := map[string]bool{}
	for _, n := range a.registry.Names() {
		known[n] = true
	}
	failed := false
	for field, d := range drivers {
		if !known[d] {
			_, _ = fmt.Fprintf(stderr, "error: %s: unknown runtime driver %q (available: %s)\n",
				field, d, strings.Join(a.registry.Names(), ", "))
			failed = true
		}
	}
	if failed {
		return ExitFailure
	}
	_, _ = fmt.Fprintf(stdout, "%s: OK (%d apps, %d targets)\n", *path, len(cfg.Apps), len(cfg.Targets))
	return ExitOK
}
