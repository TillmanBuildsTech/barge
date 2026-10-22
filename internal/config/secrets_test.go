package config_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/TillmanBuildsTech/barge/internal/config"
	"github.com/TillmanBuildsTech/barge/internal/domain"
)

// withSecrets inserts a secrets block into the first app of validYAML.
func withSecrets(block string) string {
	return strings.Replace(validYAML, "    restart: unless-stopped\n", "    restart: unless-stopped\n    secrets:\n"+block, 1)
}

func TestSecretsDecode(t *testing.T) {
	cfg, err := load(t, withSecrets("      DB_PASSWORD: {env: PROD_DB_PASSWORD}\n      API_KEY: {file: ~/.config/barge/api_key}\n"))
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Apps[0].Secrets
	if s["DB_PASSWORD"].Env != "PROD_DB_PASSWORD" || s["API_KEY"].File != "~/.config/barge/api_key" {
		t.Errorf("secrets = %+v", s)
	}
}

func TestSecretsStaySeparateFromTheDomainSpec(t *testing.T) {
	// Loading is pure and must never resolve (or even mention) secret values.
	cfg, err := load(t, withSecrets("      DB_PASSWORD: {env: PROD_DB_PASSWORD}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Apps[0].Spec().Env["DB_PASSWORD"]; ok {
		t.Error("Spec() must not contain secrets; they are resolved separately at plan time")
	}
}

func TestSecretsRejectBadInput(t *testing.T) {
	tests := []struct{ name, block, want string }{
		{"neither source", "      X: {}\n", "exactly one"},
		{"both sources", "      X: {env: A, file: /f}\n", "exactly one"},
		{"typo in source key is caught by strict decoding", "      X: {fil: /f}\n", `unknown field "fil"`},
		{"duplicate key", "      X: {env: A, env: B}\n", "duplicate key"},
		{"non-string value", "      X: {env: [A]}\n", "must be a string"},
		{"literal value instead of a reference", "      X: hunter2\n", "secrets"},
		{"bad secret name", "      \"A B\": {env: A}\n", "A B"},
		{"reserved secret name", "      DOCKER_HOST: {env: A}\n", "reserved"},
		{"bad source env var name", "      X: {env: \"A=B\"}\n", "A=B"},
		{"empty file path", "      X: {file: \"\"}\n", "exactly one"},
		{"collides with a literal env var", "      LOG_LEVEL: {env: A}\n", "LOG_LEVEL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(t, withSecrets(tt.block))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want error containing %q, got %v", tt.want, err)
			}
			if !errors.Is(err, domain.ErrInvalid) && !strings.Contains(err.Error(), "parse") {
				t.Errorf("should wrap ErrInvalid: %v", err)
			}
		})
	}
}

func TestSecretNamesAreSortedAndDeterministic(t *testing.T) {
	cfg, err := load(t, withSecrets("      Z: {env: A}\n      B: {env: A}\n      M: {env: A}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.Apps[0].SecretNames(), ","); got != "B,M,Z" {
		t.Errorf("SecretNames() = %q", got)
	}
}

func TestExampleConfigIsValid(t *testing.T) {
	// The documented example must always load: docs that don't parse are bugs.
	if _, err := config.LoadFile("../../barge.example.yaml"); err != nil {
		t.Fatalf("barge.example.yaml: %v", err)
	}
}
