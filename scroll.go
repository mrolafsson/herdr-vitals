package main

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Scrolling the screens that can be taller than the room for them: a
// machine's details (with one machine, the AI accounts too) and the Agents
// screen. Their graphs shrink first; when even the smallest don't leave
// room, they scroll, saying how much is above and below.

// bodyHeight is the room between the header and the keys.
func (m model) bodyHeight() int { return max(1, m.height-3) }

// scrolls is whether the screen shown scrolls (rather than moving a
// selection, as the cards do, or having its own, as the processes do).
func (m model) scrolls() bool {
	return (m.detail || m.aiView) && !m.procs && len(m.hosts) > 0
}

// body is the shown screen's lines, all of them.
func (m model) body() []string {
	h := m.bodyHeight()
	switch {
	case m.aiView:
		return m.viewAI(h)
	case m.detail:
		return m.viewDetail(m.hosts[m.cursor], h)
	}
	return nil
}

// window is lines from the scrolled-to one, as many as fit, with a line
// saying what's above and what's below when there's more.
func (m model) window(lines []string, height int) []string {
	if len(lines) <= height {
		return lines
	}
	top := max(0, min(m.scrollY, len(lines)-height))
	out := append([]string(nil), lines[top:top+height]...)
	if top > 0 {
		out[0] = "  " + styleDim.Render("↑ "+itoa(top)+" more · pgup")
	}
	if below := len(lines) - top - height; below > 0 {
		out[len(out)-1] = "  " + styleDim.Render("↓ "+itoa(below+1)+" more · pgdn")
	}
	return out
}

// scrollBy scrolls n lines (up when negative), no further than the end.
func (m *model) scrollBy(n int) {
	limit := max(0, len(m.body())-m.bodyHeight())
	m.scrollY = max(0, min(limit, m.scrollY+n))
}

// scrollKey is a key that scrolls, on a screen that does.
func (m model) scrollKey(k tea.KeyMsg) (tea.Model, bool) {
	page := max(1, m.bodyHeight()-2)
	switch k.String() {
	case "pgup", "ctrl+u":
		m.scrollBy(-page)
	case "pgdown", "ctrl+d":
		m.scrollBy(page)
	case "home", "g":
		m.scrollY = 0
	case "end", "G":
		m.scrollBy(len(m.body()))
	case "up", "k":
		if m.aiView {
			return m, false // moves between sessions, the view after it
		}
		m.scrollBy(-1)
	case "down", "j":
		if m.aiView {
			return m, false
		}
		m.scrollBy(1)
	default:
		return m, false
	}
	return m, true
}
