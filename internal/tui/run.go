package tui

import (
	"context"
	"fmt"
	"io"

	tea "charm.land/bubbletea/v2"
)

// Run shows the TUI until the user quits and returns the exit code to use.
// SIGINT/SIGTERM arrive through ctx: while applying they cancel gracefully
// (rolling back the app in flight) instead of killing the process mid-change.
//
// While it runs, nothing else may write to out: every other part of barge keeps
// its output out of the way, and the plain-text summary is printed only after
// the alternate screen has closed.
func Run(ctx context.Context, cfg Config, in io.Reader, out io.Writer, opts ...tea.ProgramOption) (int, error) {
	if cfg.Context == nil {
		cfg.Context = ctx
	}
	m := New(cfg)
	all := append([]tea.ProgramOption{
		tea.WithInput(in),
		tea.WithOutput(out),
		tea.WithoutSignalHandler(), // the caller's signal.NotifyContext owns signals
	}, opts...)
	p := tea.NewProgram(m, all...)

	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			p.Send(CancelMsg{})
		case <-finished:
		}
	}()

	final, err := p.Run()
	if err != nil {
		return 1, fmt.Errorf("run tui: %w", err)
	}
	fm, ok := final.(*Model)
	if !ok {
		return 1, fmt.Errorf("run tui: unexpected final model %T", final)
	}
	_, _ = fmt.Fprintln(out, fm.Summary())
	return fm.ExitCode(), nil
}
