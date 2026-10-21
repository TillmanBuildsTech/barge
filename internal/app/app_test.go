package app_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TillmanBuildsTech/barge/internal/app"
)

func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	a := app.New("v1.2.3", app.DefaultRegistry())
	code = a.Run(t.Context(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "barge.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const good = `
version: 1
runtime: {driver: docker}
apps:
  - {name: web, image: "nginx:1.27"}
targets:
  - {name: dev, type: local, apps: [web]}
`

func TestVersion(t *testing.T) {
	code, out, _ := run(t, "version")
	if code != 0 || strings.TrimSpace(out) != "barge v1.2.3" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestDriversListsBuiltins(t *testing.T) {
	code, out, _ := run(t, "drivers")
	if code != 0 || strings.TrimSpace(out) != "docker\npodman" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name     string
		args     func(t *testing.T) []string
		wantCode int
		wantOut  string // substring of stdout
		wantErr  string // substring of stderr
	}{
		{"valid", func(t *testing.T) []string { return []string{"validate", "-config", writeConfig(t, good)} }, 0, "OK", ""},
		{"invalid yaml content", func(t *testing.T) []string {
			return []string{"validate", "-config", writeConfig(t, "version: 9")}
		}, 1, "", "version"},
		{"unknown runtime driver fails fast", func(t *testing.T) []string {
			return []string{"validate", "-config", writeConfig(t, strings.Replace(good, "docker", "rkt", 1))}
		}, 1, "", "rkt"},
		{"unknown driver in target override fails fast", func(t *testing.T) []string {
			return []string{"validate", "-config", writeConfig(t, strings.Replace(good, "{name: dev, type: local", "{name: dev, type: local, runtime: {driver: nerdctl}", 1))}
		}, 1, "", "nerdctl"},
		{"missing file", func(t *testing.T) []string { return []string{"validate", "-config", "/nonexistent/barge.yaml"} }, 1, "", "open config"},
		{"missing -config flag is a usage error", func(*testing.T) []string { return []string{"validate"} }, 2, "", "-config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out, errs := run(t, tt.args(t)...)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr=%q)", code, tt.wantCode, errs)
			}
			if !strings.Contains(out, tt.wantOut) {
				t.Errorf("stdout %q missing %q", out, tt.wantOut)
			}
			if !strings.Contains(errs, tt.wantErr) {
				t.Errorf("stderr %q missing %q", errs, tt.wantErr)
			}
		})
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{nil, {"frobnicate"}} {
		code, _, errs := run(t, args...)
		if code != 2 || !strings.Contains(errs, "usage") {
			t.Errorf("args=%v code=%d stderr=%q", args, code, errs)
		}
	}
}
