package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"

	"github.com/TillmanBuildsTech/barge/internal/session"
	"github.com/TillmanBuildsTech/barge/internal/tui"
)

// TUIRunner runs the interactive UI and returns the process exit code.
type TUIRunner func(ctx context.Context, cfg tui.Config) (int, error)

// ErrNoTerminal is returned when the interactive UI is asked for without a
// terminal on stdin and stdout.
var ErrNoTerminal = errors.New("barge tui needs an interactive terminal on stdin and stdout; " +
	"for scripts and CI use `barge plan` and `barge apply` instead")

// SetTUIRunner replaces the function that runs the interactive UI. It exists
// for tests; the default drives the real terminal.
func (a *App) SetTUIRunner(r TUIRunner) { a.tuiRunner = r }

// defaultTUI runs the real UI on the process's terminal. It refuses to start
// on a pipe or file, where escape codes would be garbage and keystrokes would
// never arrive.
func defaultTUI(ctx context.Context, cfg tui.Config) (int, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) { //nolint:gosec // fds are small non-negative ints
		return 0, ErrNoTerminal
	}
	return tui.Run(ctx, cfg, os.Stdin, os.Stdout)
}

// tuiCmd implements `barge tui`. All validation happens before the screen is
// taken over, and nothing is written to stdout around the UI.
func (a *App) tuiCmd(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "", "path to the barge config file")
	target := fs.String("target", "", "only this target (default: all)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *path == "" {
		_, _ = fmt.Fprintln(stderr, "tui: -config is required")
		return ExitUsage
	}
	cfg, code, ok := a.loadConfig(*path, stderr)
	if !ok {
		return code
	}
	targets, err := session.SelectTargets(cfg, *target)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "tui: %v\n", err)
		return ExitUsage
	}

	sess := session.New(cfg, a.registry, targets)
	defer func() { _ = sess.Close() }()
	exit, err := a.tuiRunner(ctx, tui.Config{Version: a.version, ConfigPath: *path, Session: sess})
	switch {
	case errors.Is(err, ErrNoTerminal):
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitUsage
	case err != nil:
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitFailure
	}
	return exit
}
