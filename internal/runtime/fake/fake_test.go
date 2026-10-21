package fake_test

import (
	"testing"

	"github.com/TillmanBuildsTech/barge/internal/runtime"
	"github.com/TillmanBuildsTech/barge/internal/runtime/fake"
	"github.com/TillmanBuildsTech/barge/internal/runtime/runtimetest"
)

func TestFakeSatisfiesContract(t *testing.T) {
	runtimetest.Run(t, func(t *testing.T) runtime.Runtime { return fake.New() })
}
