package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/TillmanBuildsTech/barge/internal/config"
	"github.com/TillmanBuildsTech/barge/internal/domain"
)

func TestDeployDefaults(t *testing.T) {
	cfg, err := load(t, validYAML)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Deploy.Settle.Duration != config.DefaultSettle || cfg.Deploy.StopTimeout.Duration != config.DefaultStopTimeout {
		t.Errorf("deploy defaults = %+v", cfg.Deploy)
	}
}

func TestDeployOverrides(t *testing.T) {
	cfg, err := load(t, validYAML+"deploy:\n  settle: 30s\n  stop_timeout: 2m\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Deploy.Settle.Duration != 30*time.Second || cfg.Deploy.StopTimeout.Duration != 2*time.Minute {
		t.Errorf("deploy = %+v", cfg.Deploy)
	}
}

func TestDeployRejectsNegativeAndUnknown(t *testing.T) {
	for name, extra := range map[string]string{
		"negative settle":       "deploy:\n  settle: -1s\n",
		"negative stop_timeout": "deploy:\n  stop_timeout: -5s\n",
		"unknown field":         "deploy:\n  strategy: blue-green\n",
		"bad duration":          "deploy:\n  settle: soon\n",
	} {
		if _, err := load(t, validYAML+extra); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestReservedLabelsRejected(t *testing.T) {
	for _, key := range []string{"barge.app", "managed-by", "barge.spec-hash"} {
		y := strings.Replace(validYAML, "    restart: unless-stopped\n", "    restart: unless-stopped\n    labels:\n      "+key+": evil\n", 1)
		_, err := load(t, y)
		if err == nil || !strings.Contains(err.Error(), "reserved") || !strings.Contains(err.Error(), key) {
			t.Errorf("label %q: want a 'reserved' error naming it, got %v", key, err)
		}
		if err != nil && !isInvalid(err) {
			t.Errorf("must wrap ErrInvalid: %v", err)
		}
	}
}

func isInvalid(err error) bool { return strings.Contains(err.Error(), domain.ErrInvalid.Error()) }
