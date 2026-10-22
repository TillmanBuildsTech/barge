package tui_test

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/TillmanBuildsTech/barge/internal/tui"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ansi.Strip(s.b.String())
}

func waitFor(t *testing.T, out *syncBuf, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.text(), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q; output so far:\n%s", want, out.text())
}

// This runs the real Bubble Tea program: real key parsing, real renderer, real
// command scheduling and a real quit.
func TestRunEndToEndThroughTheRealProgram(t *testing.T) {
	fx := newFixture(t, baseCfg)
	inR, inW := io.Pipe()
	defer func() { _ = inW.Close() }()
	out := &syncBuf{}

	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		code, err := tui.Run(t.Context(), tui.Config{Version: "v1", ConfigPath: "barge.yaml", Session: fx.sess, NoAnimation: true},
			inR, out, tea.WithWindowSize(100, 30))
		done <- result{code, err}
	}()

	waitFor(t, out, "a apply")
	if len(fx.containers()) != 0 {
		t.Fatal("nothing may be deployed before the user asks")
	}
	_, _ = inW.Write([]byte("a"))
	waitFor(t, out, "(y/n)")
	_, _ = inW.Write([]byte("y"))
	waitFor(t, out, "Done: 2 applied")
	_, _ = inW.Write([]byte("q"))

	select {
	case r := <-done:
		if r.err != nil || r.code != 0 {
			t.Fatalf("code=%d err=%v", r.code, r.err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the program did not exit after q")
	}
	if got := len(fx.containers()); got != 2 {
		t.Errorf("containers = %d, want 2", got)
	}
	if !strings.Contains(out.text(), "barge: 2 applied") {
		t.Errorf("the scrollback summary is missing:\n%s", out.text())
	}
}

func TestRunCancelledContextQuitsAnIdleUI(t *testing.T) {
	fx := newFixture(t, baseCfg)
	ctx, cancel := contextWithCancel(t)
	inR, inW := io.Pipe()
	defer func() { _ = inW.Close() }()
	out := &syncBuf{}
	done := make(chan int, 1)
	go func() {
		code, _ := tui.Run(ctx, tui.Config{Session: fx.sess, NoAnimation: true}, inR, out, tea.WithWindowSize(80, 24))
		done <- code
	}()
	waitFor(t, out, "a apply")
	cancel() // SIGTERM
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("SIGTERM must end an idle TUI")
	}
	if len(fx.containers()) != 0 {
		t.Error("nothing should have been deployed")
	}
}
