// Package secrets resolves secret references from the config into values at
// plan time, and redacts those values from anything barge prints.
//
// Secrets are never part of config loading (which stays pure) and never appear
// in errors: messages name the secret and where it should come from, not what
// it contains.
package secrets

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/TillmanBuildsTech/barge/internal/config"
	"github.com/TillmanBuildsTech/barge/internal/domain"
)

// MaxFileSize bounds a secret file. It stops a mistyped path (a log, a device)
// from being slurped into memory and an env var.
const MaxFileSize = 64 << 10

// Resolver turns an app's secret references into a deployable spec.
type Resolver struct {
	// LookupEnv defaults to os.LookupEnv.
	LookupEnv func(string) (string, bool)
}

// Resolve returns the app's spec with its literal env plus every resolved
// secret, and the secret values (for building a Redactor). All problems are
// reported together, in a stable order. The app is not modified.
func (r Resolver) Resolve(app config.App) (domain.ContainerSpec, []string, error) {
	spec := app.Spec()
	env := make(map[string]string, len(app.Env)+len(app.Secrets))
	for k, v := range app.Env {
		env[k] = v
	}
	var (
		errs   []error
		values []string
	)
	for _, name := range app.SecretNames() {
		v, err := r.one(app.Secrets[name])
		if err != nil {
			errs = append(errs, fmt.Errorf("secret %q: %w", name, err))
			continue
		}
		env[name] = v
		values = append(values, v)
	}
	if err := errors.Join(errs...); err != nil {
		return domain.ContainerSpec{}, nil, fmt.Errorf("app %q: %w", app.Name, err)
	}
	if len(env) > 0 {
		spec.Env = env
	}
	return spec, values, nil
}

func (r Resolver) one(ref config.SecretRef) (string, error) {
	if (ref.Env == "") == (ref.File == "") {
		return "", fmt.Errorf("%w: set exactly one of env or file", domain.ErrInvalid)
	}
	var (
		v   string
		err error
	)
	if ref.Env != "" {
		v, err = r.fromEnv(ref.Env)
	} else {
		v, err = fromFile(ref.File)
	}
	if err != nil {
		return "", err
	}
	if strings.ContainsRune(v, 0) {
		return "", fmt.Errorf("%w: value contains a NUL byte", domain.ErrInvalid)
	}
	if v == "" {
		return "", fmt.Errorf("%w: value is empty", domain.ErrInvalid)
	}
	return v, nil
}

func (r Resolver) fromEnv(name string) (string, error) {
	lookup := r.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	v, ok := lookup(name)
	if !ok {
		return "", fmt.Errorf("%w: environment variable %q is not set", domain.ErrNotFound, name)
	}
	return v, nil
}

func fromFile(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand %q: %w", path, err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	f, err := os.Open(path) //nolint:gosec // the user's configured secret path
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: file %q does not exist", domain.ErrNotFound, path)
		}
		return "", fmt.Errorf("open secret file: %w", err)
	}
	defer func() { _ = f.Close() }()

	// Stat the open descriptor, not the path, so the check cannot be raced.
	// Stat follows symlinks, which Kubernetes and Docker secret mounts use.
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat secret file %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %q is not a regular file", domain.ErrInvalid, path)
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err != nil {
		return "", fmt.Errorf("read secret file %q: %w", path, err)
	}
	if len(b) > MaxFileSize {
		return "", fmt.Errorf("%w: file %q is larger than %d bytes", domain.ErrInvalid, path, MaxFileSize)
	}
	// Editors and `echo` add one line terminator that is not part of the value.
	v := strings.TrimSuffix(string(b), "\n")
	v = strings.TrimSuffix(v, "\r")
	return v, nil
}

// Mask replaces redacted values.
const Mask = "***"

// minRedactLen: shorter values would shred unrelated output ("1" would blank
// every digit one). They are never printed by barge in the first place; the
// redactor is a backstop for text from other tools, such as container CLI
// stderr.
const minRedactLen = 4

// Redactor removes known secret values from text.
type Redactor struct{ r *strings.Replacer }

// NewRedactor builds a Redactor. Longer values are matched first, so a secret
// that contains another is masked whole.
func NewRedactor(values []string) *Redactor {
	seen := map[string]bool{}
	var uniq []string
	for _, v := range values {
		if len(v) >= minRedactLen && !seen[v] {
			seen[v] = true
			uniq = append(uniq, v)
		}
	}
	if len(uniq) == 0 {
		return &Redactor{}
	}
	sort.Slice(uniq, func(i, j int) bool {
		if len(uniq[i]) != len(uniq[j]) {
			return len(uniq[i]) > len(uniq[j])
		}
		return uniq[i] < uniq[j]
	})
	pairs := make([]string, 0, 2*len(uniq))
	for _, v := range uniq {
		pairs = append(pairs, v, Mask)
	}
	return &Redactor{r: strings.NewReplacer(pairs...)}
}

// Redact returns s with every known secret value masked.
func (r *Redactor) Redact(s string) string {
	if r == nil || r.r == nil {
		return s
	}
	return r.r.Replace(s)
}
