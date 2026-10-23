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
	"github.com/TillmanBuildsTech/barge/internal/secrets"
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
	version   string
	registry  *runtime.Registry
	tuiRunner TUIRunner
}

// New builds an App.
func New(version string, reg *runtime.Registry) *App {
	return &App{version: version, registry: reg, tuiRunner: defaultTUI}
}

const usage = `usage: barge <command> [flags]

commands:
  version                          print the version
  drivers                          list available runtime drivers
  validate -config F [-resolve-secrets]
                                   validate a config file; with -resolve-secrets also
                                   check every secret reference resolves
  plan     -config F [-target T] [-json]
                                   show what apply would change (changes nothing)
  apply    -config F [-target T] [-json] [-dry-run]
                                   deploy; -dry-run is the same as plan
  tui      -config F [-target T]   interactive plan, review and apply
                                   (needs a terminal; use plan/apply in scripts)

exit codes: 0 success, 1 failure, 2 usage error
`

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
	case "plan":
		return a.deployCmd(ctx, "plan", args[1:], stdout, stderr)
	case "apply":
		return a.deployCmd(ctx, "apply", args[1:], stdout, stderr)
	case "tui":
		return a.tuiCmd(ctx, args[1:], stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command %q\n%s", args[0], usage)
		return ExitUsage
	}
}

// loadConfig reads and fully validates the config, including that every
// runtime driver it names exists in this binary, so mistakes surface before
// anything is contacted. It reports problems on stderr and returns the exit
// code to use when ok is false.
func (a *App) loadConfig(path string, stderr io.Writer) (cfg *config.Config, code int, ok bool) {
	cfg, err := config.LoadFile(path)
	if err != nil {
		// A config can have several problems, one per line: prefix each.
		for _, line := range strings.Split(strings.TrimSpace(err.Error()), "\n") {
			_, _ = fmt.Fprintf(stderr, "error: %s\n", strings.TrimSpace(line))
		}
		return nil, ExitFailure, false
	}
	known := map[string]bool{}
	for _, n := range a.registry.Names() {
		known[n] = true
	}
	failed := false
	check := func(field, driver string) {
		if !known[driver] {
			_, _ = fmt.Fprintf(stderr, "error: %s: unknown runtime driver %q (available: %s)\n",
				field, driver, strings.Join(a.registry.Names(), ", "))
			failed = true
		}
	}
	check("runtime.driver", cfg.Runtime.Driver)
	for i, t := range cfg.Targets {
		check(fmt.Sprintf("targets[%d].runtime.driver", i), t.Runtime.Driver)
	}
	if failed {
		return nil, ExitFailure, false
	}
	return cfg, ExitOK, true
}

func (a *App) validate(_ context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "", "path to the barge config file")
	resolve := fs.Bool("resolve-secrets", false, "also check that every secret reference resolves (values are never printed)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *path == "" {
		_, _ = fmt.Fprintln(stderr, "validate: -config is required")
		return ExitUsage
	}
	cfg, code, ok := a.loadConfig(*path, stderr)
	if !ok {
		return code
	}
	_, _ = fmt.Fprintf(stdout, "%s: OK (%d apps, %d targets)\n", *path, len(cfg.Apps), len(cfg.Targets))
	for _, name := range cfg.UnusedApps() {
		_, _ = fmt.Fprintf(stderr, "warning: app %q is not used by any target, so it will never be deployed\n", name)
	}
	if *resolve {
		n, failed := 0, false
		for _, app := range cfg.Apps {
			if _, _, err := (secrets.Resolver{}).Resolve(app); err != nil {
				_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
				failed = true
			}
			n += len(app.Secrets)
		}
		if failed {
			return ExitFailure
		}
		_, _ = fmt.Fprintf(stdout, "%d secrets resolved\n", n)
	}
	return ExitOK
}
