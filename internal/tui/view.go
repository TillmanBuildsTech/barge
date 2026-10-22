package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Colours use the terminal's own 16-colour palette so they follow the user's
// theme. Meaning is never carried by colour alone: every state also has a
// symbol and a word.
var (
	sBold   = lipgloss.NewStyle().Bold(true)
	sDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	sGreen  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	sYellow = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	sCyan   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	sRed    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	sRedB   = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const (
	headerLines  = 2
	footerLines  = 2
	indent       = "  "
	detailIndent = "        "
)

// View implements tea.Model.
func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "barge"
	return v
}

// safe is the single gate for every dynamic string: it masks known secret
// values BEFORE any wrapping or truncation, so layout can never split a secret
// into pieces the redactor would miss.
func (m *Model) safe(s string) string { return m.red.Redact(s) }

func (m *Model) width() int {
	if m.w <= 0 {
		return 1000
	}
	return m.w
}

func (m *Model) fit(line string) string { return ansi.Truncate(line, m.width(), "…") }

// wrap breaks text into lines of at most the given width (never below 1).
func wrapText(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	out := make([]string, 0, strings.Count(s, "\n")+1)
	for _, para := range strings.Split(s, "\n") {
		out = append(out, strings.Split(ansi.Wrap(para, width, " "), "\n")...)
	}
	return out
}

func (m *Model) spinner() string {
	if m.cfg.NoAnimation {
		return spinnerFrames[0]
	}
	return spinnerFrames[m.frame%len(spinnerFrames)]
}

func (m *Model) render() string {
	body := m.body()
	avail := m.bodyHeight()
	off := m.offset
	if len(body) > avail {
		if off > len(body)-avail {
			off = len(body) - avail
		}
		if off < 0 {
			off = 0
		}
		body = body[off : off+avail]
	}
	out := make([]string, 0, headerLines+len(body)+footerLines)
	out = append(out, sBold.Render("barge")+sDim.Render(" "+m.safe(m.cfg.Version)+" · "+m.safe(m.cfg.ConfigPath)), "")
	out = append(out, body...)

	notice := ""
	if m.notice != "" {
		notice = sYellow.Render(m.notice)
	}
	out = append(out, m.fit(notice), m.fit(m.help()))

	for i := range out {
		out[i] = m.fit(out[i])
	}
	return strings.Join(out, "\n")
}

func (m *Model) bodyHeight() int {
	if m.h <= 0 {
		return 1 << 30
	}
	n := m.h - headerLines - footerLines
	if n < 1 {
		n = 1
	}
	return n
}

func (m *Model) page() int {
	if h := m.bodyHeight(); h < 1<<20 {
		return h - 1
	}
	return 10
}

func (m *Model) scroll(d int) {
	m.offset += d
	m.clamp()
}

func (m *Model) clamp() {
	limit := len(m.body()) - m.bodyHeight()
	if m.offset > limit {
		m.offset = limit
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m *Model) help() string {
	switch m.phase {
	case phasePlanning:
		return sDim.Render("q quit")
	case phaseReview:
		if !m.planOK {
			return sDim.Render("↑/↓ scroll · r re-plan · q quit")
		}
		return sDim.Render("a apply · r re-plan · ↑/↓ scroll · q quit")
	case phaseConfirm:
		n, t := m.changes, len(m.targets)
		return sBold.Render(fmt.Sprintf("Apply %d %s to %d %s? (y/n)", n, plural(n, "change"), t, plural(t, "target")))
	case phaseApplying:
		if m.cancelling {
			return sYellow.Render("Cancelling: finishing the current step and rolling back…")
		}
		return sDim.Render("ctrl+c cancel (rolls back the app in progress)")
	case phaseDone:
		return sDim.Render("r re-plan · ↑/↓ scroll · q quit")
	}
	return ""
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// body renders the scrollable middle of the screen as lines.
func (m *Model) body() []string {
	if m.phase == phasePlanning {
		return []string{sCyan.Render(m.spinner()) + " Planning: resolving secrets and checking each target…"}
	}
	var lines []string
	for i, t := range m.targets {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, m.targetLines(t)...)
	}
	lines = append(lines, "", m.summaryLine())
	if m.applyErr != "" {
		for _, l := range wrapText(m.safe(m.applyErr), m.width()-len(indent)) {
			lines = append(lines, indent+sRed.Render(l))
		}
	}
	return lines
}

func (m *Model) targetLines(t *targetView) []string {
	head := sBold.Render("Target "+m.safe(t.name)) + sDim.Render(fmt.Sprintf("  (%s, %s)", m.safe(t.driver), m.safe(t.typ)))
	lines := []string{head}
	if t.err != "" {
		for i, l := range wrapText(m.safe(t.err), m.width()-len(indent)-2) {
			prefix := "✗ "
			if i > 0 {
				prefix = "  "
			}
			lines = append(lines, indent+sRed.Render(prefix+l))
		}
		return lines
	}
	appW := 8
	for _, it := range t.items {
		if n := ansi.StringWidth(it.app); n > appW {
			appW = n
		}
	}
	if appW > 24 {
		appW = 24
	}
	if len(t.items) == 0 {
		lines = append(lines, indent+sDim.Render("(no apps)"))
	}
	for _, it := range t.items {
		lines = append(lines, m.itemLines(it, appW)...)
	}
	return lines
}

func pad(s string, w int) string {
	s = ansi.Truncate(s, w, "…")
	return s + strings.Repeat(" ", w-ansi.StringWidth(s))
}

func (m *Model) itemLines(it *itemView, appW int) []string {
	app := pad(m.safe(it.app), appW)
	label := it.action
	if it.action == "noop" {
		label = "unchanged"
	}
	label = pad(label, 9)

	var sym, detail string
	var style lipgloss.Style
	switch it.status {
	case stPlanned:
		sym, style = planSymbol(it.action)
		detail = m.safe(it.reason)
		if it.action == "blocked" {
			style = sRed
		}
	case stPending:
		sym, style, detail = "·", sDim, "waiting"
	case stRunning:
		sym, style, detail = m.spinner(), sCyan, m.safe(it.step)
	case stApplied:
		sym, style, detail = "✓", sGreen, "applied"
	case stUnchanged:
		sym, style, detail = "=", sDim, "up to date"
	case stFailed:
		sym, style, detail = "✗", sRedB, "FAILED"
	case stSkipped:
		sym, style, detail = "-", sYellow, "skipped"
	}
	lines := []string{indent + style.Render(sym) + " " + app + " " + style.Render(label) + " " + detail}

	extra := func(s lipgloss.Style, text string) {
		for _, l := range wrapText(m.safe(text), m.width()-len(detailIndent)) {
			lines = append(lines, detailIndent+s.Render(l))
		}
	}
	if it.status == stFailed || it.status == stSkipped {
		if it.err != "" {
			extra(sRed, it.err)
		}
		switch {
		case it.rollbackErr != "":
			extra(sRedB, "ROLLBACK FAILED: "+it.rollbackErr)
		case it.rolledBack:
			extra(sGreen, "rolled back: the previous version is running again")
		}
	}
	for _, w := range it.warnings {
		extra(sYellow, "warning: "+w)
	}
	return lines
}

func planSymbol(action string) (sym string, style lipgloss.Style) {
	switch action {
	case "create":
		return "+", sGreen
	case "replace":
		return "~", sYellow
	case "start":
		return ">", sCyan
	case "blocked":
		return "!", sRed
	default:
		return "=", sDim
	}
}

func (m *Model) summaryLine() string {
	if m.phase == phaseApplying {
		a, _, f, _ := m.counts()
		return sBold.Render(fmt.Sprintf("Applying… %d done, %d failed", a, f))
	}
	if m.phase == phaseDone {
		a, u, f, s := m.counts()
		var parts []string
		for _, p := range []struct {
			n    int
			what string
		}{{a, "applied"}, {u, "unchanged"}, {f, "failed"}, {s, "skipped"}} {
			if p.n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", p.n, p.what))
			}
		}
		if len(parts) == 0 {
			parts = []string{"nothing changed"}
		}
		st := sGreen
		if m.ExitCode() != 0 {
			st = sRed
		}
		return st.Render("Done: " + strings.Join(parts, " · "))
	}
	problems := 0
	for _, t := range m.targets {
		if t.err != "" {
			problems++
		}
		for _, it := range t.items {
			if it.action == "blocked" {
				problems++
			}
		}
	}
	switch {
	case problems > 0:
		return sRed.Render(fmt.Sprintf("%d %s · %d %s", m.changes, plural(m.changes, "change"), problems, plural(problems, "problem")))
	case m.changes == 0:
		return sGreen.Render("Everything is up to date.")
	default:
		return sBold.Render(fmt.Sprintf("%d %s", m.changes, plural(m.changes, "change")))
	}
}
