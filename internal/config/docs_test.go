package config_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/TillmanBuildsTech/barge/internal/config"
)

var yamlFence = regexp.MustCompile("(?s)```yaml\\n(.*?)```")

// Every complete config shown in docs/ (a yaml block starting with
// "version:") must load. Documentation that does not parse is a bug, and this
// is what stops docs from drifting away from the code.
func TestDocsConfigExamplesLoad(t *testing.T) {
	files, err := filepath.Glob("../../docs/*.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no docs found: the test is looking in the wrong place")
	}
	examples := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, m := range yamlFence.FindAllStringSubmatch(string(b), -1) {
			if !strings.HasPrefix(strings.TrimSpace(m[1]), "version:") {
				continue // a fragment, not a whole config
			}
			examples++
			if _, err := config.Load(strings.NewReader(m[1])); err != nil {
				t.Errorf("%s: example #%d does not load: %v\n%s", filepath.Base(f), i+1, err, m[1])
			}
		}
	}
	if examples == 0 {
		t.Fatal("docs contain no complete config example; the guard is not guarding anything")
	}
}
