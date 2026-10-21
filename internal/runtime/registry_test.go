package runtime_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/TillmanBuildsTech/barge/internal/domain"
	"github.com/TillmanBuildsTech/barge/internal/runtime"
	"github.com/TillmanBuildsTech/barge/internal/runtime/fake"
)

func factory(runtime.Deps) (runtime.Runtime, error) { return fake.New(), nil }

func TestRegistry(t *testing.T) {
	r := runtime.NewRegistry()
	if err := r.Register("zeta", factory); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("alpha", factory); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("alpha", factory); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("duplicate register: want ErrConflict, got %v", err)
	}
	if err := r.Register("", factory); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("empty name: want ErrInvalid, got %v", err)
	}
	if err := r.Register("nilf", nil); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("nil factory: want ErrInvalid, got %v", err)
	}
	if got := strings.Join(r.Names(), ","); got != "alpha,zeta" {
		t.Errorf("Names() = %q, want sorted alpha,zeta", got)
	}
	if _, err := r.New("alpha", runtime.Deps{}); err != nil {
		t.Errorf("New(alpha): %v", err)
	}
	_, err := r.New("rkt", runtime.Deps{})
	if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "alpha, zeta") {
		t.Errorf("unknown driver should be ErrInvalid and list known drivers, got %v", err)
	}
}
