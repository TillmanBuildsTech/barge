package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/TillmanBuildsTech/barge/internal/config"
	"github.com/TillmanBuildsTech/barge/internal/deploy"
	"github.com/TillmanBuildsTech/barge/internal/domain"
	"github.com/TillmanBuildsTech/barge/internal/runtime"
	"github.com/TillmanBuildsTech/barge/internal/secrets"
	"github.com/TillmanBuildsTech/barge/internal/transport"
)

// reportSchema is the version of the -json output. Bump it on breaking changes.
const reportSchema = 1

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// executorFor builds the transport for a target: this machine, or SSH to a
// remote Linux host with verified host keys.
func executorFor(t config.Target) (transport.Executor, io.Closer, error) {
	switch t.Type {
	case config.TargetLocal:
		return transport.Local{}, nopCloser{}, nil
	case config.TargetSSH:
		s, err := transport.NewSSH(transport.SSHConfig{
			Host: t.SSH.Host, Port: t.SSH.Port, User: t.SSH.User,
			IdentityFile: t.SSH.IdentityFile, KnownHosts: t.SSH.KnownHosts,
			ConnectTimeout: t.SSH.ConnectTimeout.Duration,
		})
		if err != nil {
			return nil, nil, err
		}
		return s, s, nil
	default:
		return nil, nil, fmt.Errorf("%w: unsupported target type %q", domain.ErrInvalid, t.Type)
	}
}

// targetRun is everything known about one target during a plan/apply.
type targetRun struct {
	target  config.Target
	engine  *deploy.Engine
	closer  io.Closer
	plan    deploy.Plan
	err     error // could not connect or plan
	results []deploy.Result
	applied bool
}

func (tr *targetRun) ok() bool {
	if tr.err != nil || tr.plan.HasBlocked() {
		return false
	}
	return !tr.applied || !deploy.AnyFailed(tr.results)
}

// secretCache resolves each app's secrets at most once per run and remembers
// every value so output can be redacted.
type secretCache struct {
	resolver secrets.Resolver
	byApp    map[string]resolvedApp
	values   []string
}

type resolvedApp struct {
	spec domain.ContainerSpec
	err  error
}

func newSecretCache() *secretCache { return &secretCache{byApp: map[string]resolvedApp{}} }

func (c *secretCache) spec(app config.App) (domain.ContainerSpec, error) {
	if r, ok := c.byApp[app.Name]; ok {
		return r.spec, r.err
	}
	spec, vals, err := c.resolver.Resolve(app)
	c.byApp[app.Name] = resolvedApp{spec, err}
	c.values = append(c.values, vals...)
	return spec, err
}

func (a *App) planTarget(ctx context.Context, cfg *config.Config, t config.Target, sc *secretCache) *targetRun {
	tr := &targetRun{target: t, closer: nopCloser{}}
	// Resolve secrets first: it is local and cheap, and a deploy that cannot
	// be fully specified should not even open a connection.
	specs := make([]domain.ContainerSpec, 0, len(t.Apps))
	var specErrs []error
	for _, name := range t.Apps {
		app, _ := cfg.App(name) // existence is guaranteed by config validation
		spec, err := sc.spec(app)
		if err != nil {
			specErrs = append(specErrs, err)
			continue
		}
		specs = append(specs, spec)
	}
	if err := errors.Join(specErrs...); err != nil {
		tr.err = err
		return tr
	}
	exec, closer, err := executorFor(t)
	if err != nil {
		tr.err = err
		return tr
	}
	tr.closer = closer
	rt, err := a.registry.New(t.Runtime.Driver, runtime.Deps{Exec: exec, Options: t.Runtime.Options})
	if err != nil {
		tr.err = err
		return tr
	}
	if _, err := rt.Ping(ctx); err != nil {
		tr.err = fmt.Errorf("runtime %s: %w", t.Runtime.Driver, err)
		return tr
	}
	tr.engine = deploy.New(rt, deploy.Options{
		Settle:      cfg.Deploy.Settle.Duration,
		StopTimeout: cfg.Deploy.StopTimeout.Duration,
	})
	tr.plan, tr.err = tr.engine.Plan(ctx, specs)
	return tr
}

func selectTargets(cfg *config.Config, name string) ([]config.Target, error) {
	if name == "" {
		return cfg.Targets, nil
	}
	var known []string
	for _, t := range cfg.Targets {
		if t.Name == name {
			return []config.Target{t}, nil
		}
		known = append(known, t.Name)
	}
	return nil, fmt.Errorf("unknown target %q (available: %s)", name, strings.Join(known, ", "))
}

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
	targets, err := selectTargets(cfg, *target)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", cmd, err)
		return ExitUsage
	}

	runs := make([]*targetRun, 0, len(targets))
	defer func() {
		for _, tr := range runs {
			_ = tr.closer.Close()
		}
	}()
	allOK := true
	sc := newSecretCache()
	for _, t := range targets {
		tr := a.planTarget(ctx, cfg, t, sc)
		runs = append(runs, tr)
		allOK = allOK && tr.ok()
	}

	applying := cmd == "apply" && !*dryRun
	if applying && allOK {
		for _, tr := range runs {
			results, err := tr.engine.Apply(ctx, tr.plan)
			if err != nil { // only possible for a blocked plan, excluded above
				tr.err = err
			}
			tr.results, tr.applied = results, true
			allOK = allOK && tr.ok()
		}
	}

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
	_, _ = io.WriteString(stdout, secrets.NewRedactor(sc.values).Redact(buf.String()))
	if !allOK {
		if !*asJSON {
			_, _ = fmt.Fprintln(stderr, failureSummary(applying, runs))
		}
		return ExitFailure
	}
	return ExitOK
}

func failureSummary(applying bool, runs []*targetRun) string {
	for _, tr := range runs {
		if !tr.applied && (tr.err != nil || tr.plan.HasBlocked()) {
			if applying {
				return "error: nothing was changed: fix the problems above and re-run"
			}
			return "error: the plan has problems (see above)"
		}
	}
	return "error: some apps failed to deploy (see above)"
}

// ---- text output --------------------------------------------------------

func writeText(w io.Writer, runs []*targetRun) {
	for i, tr := range runs {
		if i > 0 {
			_, _ = fmt.Fprintln(w)
		}
		_, _ = fmt.Fprintf(w, "target %s (%s, %s)\n", tr.target.Name, tr.target.Runtime.Driver, tr.target.Type)
		switch {
		case tr.err != nil && !tr.applied:
			_, _ = fmt.Fprintf(w, "  error: %v\n", tr.err)
		case tr.applied:
			for _, r := range tr.results {
				writeResultText(w, r)
			}
		default:
			_, _ = fmt.Fprint(w, tr.plan.String())
			if len(tr.plan.Items) == 0 {
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

func writeJSON(w io.Writer, cmd string, dryRun, ok bool, runs []*targetRun) error {
	rep := report{Schema: reportSchema, Command: cmd, DryRun: dryRun, OK: ok, Targets: []targetReport{}}
	for _, tr := range runs {
		t := targetReport{Name: tr.target.Name, Runtime: tr.target.Runtime.Driver, Error: errString(tr.err), Apps: []appReport{}}
		for i, it := range tr.plan.Items {
			ar := appReport{App: it.Spec.Name, Action: string(it.Action), Reason: it.Reason}
			if tr.applied && i < len(tr.results) {
				r := tr.results[i]
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
