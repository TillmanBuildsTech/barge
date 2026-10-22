package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/TillmanBuildsTech/barge/internal/deploy"
	"github.com/TillmanBuildsTech/barge/internal/session"
)

// reportSchema is the version of the -json output. Bump it on breaking changes.
const reportSchema = 1

// deployCmd implements `plan` and `apply`. Both first plan every selected
// target. apply only proceeds if EVERY target planned cleanly with nothing
// blocked, so one unreachable or conflicting host never leaves the others
// half-deployed.
func (a *App) deployCmd(ctx context.Context, cmd string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "", "path to the barge config file")
	target := fs.String("target", "", "only this target (default: all)")
	asJSON := fs.Bool("json", false, "machine-readable output")
	dryRun := new(bool)
	if cmd == "apply" {
		dryRun = fs.Bool("dry-run", false, "show the plan without changing anything")
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *path == "" {
		_, _ = fmt.Fprintf(stderr, "%s: -config is required\n", cmd)
		return ExitUsage
	}
	cfg, code, ok := a.loadConfig(*path, stderr)
	if !ok {
		return code
	}
	targets, err := session.SelectTargets(cfg, *target)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", cmd, err)
		return ExitUsage
	}

	sess := session.New(cfg, a.registry, targets)
	defer func() { _ = sess.Close() }()
	runs := sess.Plan(ctx)

	applying := cmd == "apply" && !*dryRun
	if applying && sess.PlanOK() {
		_ = sess.Apply(ctx, nil) // cannot be refused: the plan was just checked
	}
	allOK := sess.OK()

	// Everything printed goes through one choke point that masks known secret
	// values, including text that came from the container CLI or a registry.
	var buf bytes.Buffer
	if *asJSON {
		if err := writeJSON(&buf, cmd, !applying, allOK, runs); err != nil {
			_, _ = fmt.Fprintf(stderr, "error: write report: %v\n", err)
			return ExitFailure
		}
	} else {
		writeText(&buf, runs)
	}
	_, _ = io.WriteString(stdout, sess.Redactor().Redact(buf.String()))
	if !allOK {
		if !*asJSON {
			_, _ = fmt.Fprintln(stderr, failureSummary(applying, runs))
		}
		return ExitFailure
	}
	return ExitOK
}

func failureSummary(applying bool, runs []*session.TargetRun) string {
	for _, tr := range runs {
		if !tr.Applied && !tr.PlanOK() {
			if applying {
				return "error: nothing was changed: fix the problems above and re-run"
			}
			return "error: the plan has problems (see above)"
		}
	}
	return "error: some apps failed to deploy (see above)"
}

// ---- text output --------------------------------------------------------

func writeText(w io.Writer, runs []*session.TargetRun) {
	for i, tr := range runs {
		if i > 0 {
			_, _ = fmt.Fprintln(w)
		}
		_, _ = fmt.Fprintf(w, "target %s (%s, %s)\n", tr.Target.Name, tr.Target.Runtime.Driver, tr.Target.Type)
		switch {
		case tr.Err != nil && !tr.Applied:
			_, _ = fmt.Fprintf(w, "  error: %v\n", tr.Err)
		case tr.Applied:
			for _, r := range tr.Results {
				writeResultText(w, r)
			}
		default:
			_, _ = fmt.Fprint(w, tr.Plan.String())
			if len(tr.Plan.Items) == 0 {
				_, _ = fmt.Fprintln(w, "  (no apps)")
			}
		}
	}
}

func writeResultText(w io.Writer, r deploy.Result) {
	switch r.Outcome {
	case deploy.OutcomeApplied:
		_, _ = fmt.Fprintf(w, "  %-16s applied    (%s)\n", r.App, r.Action)
	case deploy.OutcomeUnchanged:
		_, _ = fmt.Fprintf(w, "  %-16s unchanged\n", r.App)
	case deploy.OutcomeSkipped:
		_, _ = fmt.Fprintf(w, "  %-16s SKIPPED    %v\n", r.App, r.Err)
	case deploy.OutcomeFailed:
		_, _ = fmt.Fprintf(w, "  %-16s FAILED     %v\n", r.App, r.Err)
		switch {
		case r.RollbackErr != nil:
			_, _ = fmt.Fprintf(w, "    ROLLBACK FAILED: %v\n", r.RollbackErr)
		case r.RolledBack:
			_, _ = fmt.Fprintln(w, "    rolled back: the previous state was restored")
		}
	}
	for _, warn := range r.Warnings {
		_, _ = fmt.Fprintf(w, "    warning: %s\n", warn)
	}
}

// ---- JSON output --------------------------------------------------------

type report struct {
	Schema  int            `json:"schema"`
	Command string         `json:"command"`
	DryRun  bool           `json:"dry_run"`
	OK      bool           `json:"ok"`
	Targets []targetReport `json:"targets"`
}

type targetReport struct {
	Name    string      `json:"name"`
	Runtime string      `json:"runtime"`
	Error   string      `json:"error,omitempty"`
	Apps    []appReport `json:"apps"`
}

type appReport struct {
	App           string   `json:"app"`
	Action        string   `json:"action"`
	Reason        string   `json:"reason,omitempty"`
	Outcome       string   `json:"outcome,omitempty"`
	Error         string   `json:"error,omitempty"`
	RolledBack    bool     `json:"rolled_back,omitempty"`
	RollbackError string   `json:"rollback_error,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func writeJSON(w io.Writer, cmd string, dryRun, ok bool, runs []*session.TargetRun) error {
	rep := report{Schema: reportSchema, Command: cmd, DryRun: dryRun, OK: ok, Targets: []targetReport{}}
	for _, tr := range runs {
		t := targetReport{Name: tr.Target.Name, Runtime: tr.Target.Runtime.Driver, Error: errString(tr.Err), Apps: []appReport{}}
		for i, it := range tr.Plan.Items {
			ar := appReport{App: it.Spec.Name, Action: string(it.Action), Reason: it.Reason}
			if tr.Applied && i < len(tr.Results) {
				r := tr.Results[i]
				ar.Outcome, ar.Error = string(r.Outcome), errString(r.Err)
				ar.RolledBack, ar.RollbackError, ar.Warnings = r.RolledBack, errString(r.RollbackErr), r.Warnings
			}
			t.Apps = append(t.Apps, ar)
		}
		rep.Targets = append(rep.Targets, t)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		return errors.Join(errors.New("encode json"), err)
	}
	return nil
}
