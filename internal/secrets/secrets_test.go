package secrets_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/TillmanBuildsTech/barge/internal/config"
	"github.com/TillmanBuildsTech/barge/internal/domain"
	"github.com/TillmanBuildsTech/barge/internal/secrets"
)

func app(env map[string]string, sec map[string]config.SecretRef) config.App {
	return config.App{Name: "web", Image: "nginx:1", Env: env, Secrets: sec}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveFromEnvironment(t *testing.T) {
	t.Setenv("BARGE_T_DB", "pa$$ w0rd\nwith newline")
	spec, vals, err := secrets.Resolver{}.Resolve(app(
		map[string]string{"LOG_LEVEL": "info"},
		map[string]config.SecretRef{"DB_PASSWORD": {Env: "BARGE_T_DB"}},
	))
	if err != nil {
		t.Fatal(err)
	}
	if spec.Env["DB_PASSWORD"] != "pa$$ w0rd\nwith newline" || spec.Env["LOG_LEVEL"] != "info" || len(spec.Env) != 2 {
		t.Errorf("env = %v", spec.Env)
	}
	if len(vals) != 1 || vals[0] != "pa$$ w0rd\nwith newline" {
		t.Errorf("values for redaction = %q", vals)
	}
}

func TestResolveFromFileTrimsOneTrailingNewline(t *testing.T) {
	for in, want := range map[string]string{
		"s3cret":       "s3cret",
		"s3cret\n":     "s3cret",
		"s3cret\r\n":   "s3cret",
		"s3cret\n\n":   "s3cret\n", // only one terminator is removed
		"  padded  \n": "  padded  ",
	} {
		p := writeFile(t, in)
		spec, _, err := secrets.Resolver{}.Resolve(app(nil, map[string]config.SecretRef{"K": {File: p}}))
		if err != nil || spec.Env["K"] != want {
			t.Errorf("file %q -> %q (err=%v), want %q", in, spec.Env["K"], err, want)
		}
	}
}

func TestResolveExpandsHomeInFilePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows
	if err := os.WriteFile(filepath.Join(home, "tok"), []byte("tilde-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec, _, err := secrets.Resolver{}.Resolve(app(nil, map[string]config.SecretRef{"K": {File: "~/tok"}}))
	if err != nil || spec.Env["K"] != "tilde-secret" {
		t.Fatalf("env=%v err=%v", spec.Env, err)
	}
}

func TestResolveFollowsSymlinks(t *testing.T) {
	// Kubernetes and Docker secret mounts are symlink farms.
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	target := writeFile(t, "via-link")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	spec, _, err := secrets.Resolver{}.Resolve(app(nil, map[string]config.SecretRef{"K": {File: link}}))
	if err != nil || spec.Env["K"] != "via-link" {
		t.Fatalf("env=%v err=%v", spec.Env, err)
	}
}

func TestResolveFailures(t *testing.T) {
	big := writeFile(t, strings.Repeat("x", secrets.MaxFileSize+1))
	empty := writeFile(t, "")
	onlyNL := writeFile(t, "\n")
	nul := writeFile(t, "ab\x00cd")
	t.Setenv("BARGE_T_EMPTY", "")

	tests := []struct {
		name string
		ref  config.SecretRef
		is   error
		want string // must be in the message, besides the secret name
	}{
		{"unset env var", config.SecretRef{Env: "BARGE_T_DEFINITELY_UNSET"}, domain.ErrNotFound, "BARGE_T_DEFINITELY_UNSET"},
		{"empty env var", config.SecretRef{Env: "BARGE_T_EMPTY"}, domain.ErrInvalid, "empty"},
		{"missing file", config.SecretRef{File: filepath.Join(t.TempDir(), "nope")}, domain.ErrNotFound, "nope"},
		{"directory", config.SecretRef{File: t.TempDir()}, domain.ErrInvalid, "regular file"},
		{"empty file", config.SecretRef{File: empty}, domain.ErrInvalid, "empty"},
		{"newline-only file", config.SecretRef{File: onlyNL}, domain.ErrInvalid, "empty"},
		{"oversized file", config.SecretRef{File: big}, domain.ErrInvalid, "larger than"},
		{"NUL in file", config.SecretRef{File: nul}, domain.ErrInvalid, "NUL"},
		{"no source", config.SecretRef{}, domain.ErrInvalid, "exactly one"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := secrets.Resolver{}.Resolve(app(nil, map[string]config.SecretRef{"MY_SECRET": tt.ref}))
			if !errors.Is(err, tt.is) {
				t.Fatalf("want %v, got %v", tt.is, err)
			}
			if !strings.Contains(err.Error(), "MY_SECRET") || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("message should name the secret and contain %q: %v", tt.want, err)
			}
		})
	}
}

func TestResolveReportsEverythingWrongAtOnceInStableOrder(t *testing.T) {
	_, _, err := secrets.Resolver{}.Resolve(app(nil, map[string]config.SecretRef{
		"ZED":   {Env: "BARGE_T_UNSET_1"},
		"ALPHA": {Env: "BARGE_T_UNSET_2"},
	}))
	if err == nil {
		t.Fatal("want error")
	}
	msg := err.Error()
	if a, z := strings.Index(msg, "ALPHA"), strings.Index(msg, "ZED"); a < 0 || z < 0 || a > z {
		t.Errorf("both problems expected, in sorted order: %q", msg)
	}
}

func TestErrorsNeverContainSecretContent(t *testing.T) {
	const leak = "TOPSECRETCONTENT123"
	for name, ref := range map[string]config.SecretRef{
		"nul file":  {File: writeFile(t, leak+"\x00")},
		"big file":  {File: writeFile(t, leak+strings.Repeat("x", secrets.MaxFileSize))},
		"directory": {File: t.TempDir()},
	} {
		_, _, err := secrets.Resolver{}.Resolve(app(nil, map[string]config.SecretRef{"K": ref}))
		if err == nil || strings.Contains(err.Error(), leak) {
			t.Errorf("%s: error must exist and not echo content: %v", name, err)
		}
	}
}

func TestResolveDoesNotMutateTheConfigApp(t *testing.T) {
	t.Setenv("BARGE_T_X", "value-1234")
	in := app(map[string]string{"A": "1"}, map[string]config.SecretRef{"S": {Env: "BARGE_T_X"}})
	if _, _, err := (secrets.Resolver{}).Resolve(in); err != nil {
		t.Fatal(err)
	}
	if len(in.Env) != 1 {
		t.Errorf("input app's Env was modified: %v", in.Env)
	}
}

func TestResolverLookupIsInjectable(t *testing.T) {
	r := secrets.Resolver{LookupEnv: func(k string) (string, bool) { return "from-" + k, true }}
	spec, _, err := r.Resolve(app(nil, map[string]config.SecretRef{"K": {Env: "FOO"}}))
	if err != nil || spec.Env["K"] != "from-FOO" {
		t.Fatalf("env=%v err=%v", spec.Env, err)
	}
}

func TestRedactor(t *testing.T) {
	r := secrets.NewRedactor([]string{"hunter22", "hunter22-long", "ab", ""})
	for in, want := range map[string]string{
		"password is hunter22 ok":   "password is " + secrets.Mask + " ok",
		"pw=hunter22-long!":         "pw=" + secrets.Mask + "!", // longer secret wins over its prefix
		"nothing to see":            "nothing to see",
		"ab is too short to redact": "ab is too short to redact",
		"twice hunter22 hunter22":   "twice " + secrets.Mask + " " + secrets.Mask,
		"multi\nline hunter22\nend": "multi\nline " + secrets.Mask + "\nend",
	} {
		if got := r.Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
	if got := secrets.NewRedactor(nil).Redact("x"); got != "x" {
		t.Errorf("empty redactor must pass text through, got %q", got)
	}
}

func TestNULInEnvValueIsRejected(t *testing.T) {
	// The OS cannot hold such a variable, but a custom lookup (or a future
	// source) could return one; it would corrupt the remote env preamble.
	r := secrets.Resolver{LookupEnv: func(string) (string, bool) { return "a\x00b", true }}
	_, _, err := r.Resolve(app(nil, map[string]config.SecretRef{"K": {Env: "X"}}))
	if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "NUL") {
		t.Fatalf("want ErrInvalid mentioning NUL, got %v", err)
	}
}
