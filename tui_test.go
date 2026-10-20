package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// At any size, nothing is drawn past the edge or below the bottom: a line
// too long wraps in the terminal and pushes everything down.
func TestEverySizeFits(t *testing.T) {
	sizes := [][2]int{{200, 60}, {168, 46}, {120, 40}, {90, 30}, {62, 60}, {44, 30}, {30, 20}, {190, 2}, {60, 1}, {80, 8}}
	for _, md := range []mode{modePopup, modePane} {
		for _, sz := range sizes {
			m := demoModel(t, md, sz[0], sz[1])
			for view := range 5 {
				detail := view == 1 || view == 3
				m.detail, m.procs, m.day, m.aiView = detail, view == 2, view == 3, view == 4
				if m.day {
					m.readDay()
				}
				for cursor := range m.hosts {
					m.cursor = cursor
					lines := strings.Split(m.View(), "\n")
					if len(lines) > sz[1] {
						t.Errorf("%dx%d detail=%v: %d lines", sz[0], sz[1], detail, len(lines))
					}
					for i, l := range lines {
						if w := lipgloss.Width(l); w > sz[0] {
							t.Errorf("%dx%d detail=%v machine %d line %d is %d wide: %q", sz[0], sz[1], detail, cursor, i, w, l)
						}
					}
				}
			}
		}
	}
}

func TestKeysMoveAroundTheGrid(t *testing.T) {
	m := demoModel(t, modePopup, 168, 46) // four cards to a row
	press := func(k string) {
		next, _ := m.handleKey(keyMsg(k))
		m = next.(model)
	}
	press("right")
	if m.cursor != 1 {
		t.Fatalf("right: %d", m.cursor)
	}
	press("down")
	if m.cursor != m.aiIdx() { // the AI card, after edge-02 on the next row
		t.Fatalf("down: %d", m.cursor)
	}
	press("enter")
	if !m.aiView {
		t.Fatal("enter on the AI card didn't open the Agents screen")
	}
	press("esc")
	press("left")
	press("enter")
	if !m.detail {
		t.Fatal("enter didn't open the details")
	}
	press("esc")
	if m.detail {
		t.Fatal("esc didn't close the details")
	}
	// Hottest first: build-01 (hot) leads.
	press("s")
	if o := m.order(); m.hosts[o[0]].t.Name != "build-01" {
		t.Errorf("hottest first starts with %s", m.hosts[o[0]].t.Name)
	}
}

func TestTheProcessesScreen(t *testing.T) {
	m := demoModel(t, modePopup, 120, 30)
	press := func(k string) {
		next, _ := m.handleKey(keyMsg(k))
		m = next.(model)
	}
	press("t")
	if !m.procs || !strings.Contains(m.View(), "PROCESS") {
		t.Fatal("t didn't show the processes")
	}
	press("m")
	if !m.byMem {
		t.Fatal("m didn't sort by memory")
	}
	// atlas by memory: rust-analyzer's 2.1G leads.
	if p := m.shownProcs(m.hosts[0]); p[0].Name != "rust-analyzer" {
		t.Errorf("by memory, %s first", p[0].Name)
	}
	press("esc")
	if m.procs {
		t.Fatal("esc didn't go back")
	}
}

func TestAClickSelectsThenOpens(t *testing.T) {
	m := demoModel(t, modePopup, 168, 46)
	l := m.grid()
	x, y := l.x0+l.w+cardGap+3, 4 // inside the second card
	click := func() {
		next, _ := m.handleMouse(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
		m = next.(model)
	}
	click()
	if m.cursor != 1 || m.detail {
		t.Fatalf("first click: cursor %d detail %v", m.cursor, m.detail)
	}
	click()
	if !m.detail {
		t.Fatal("a click on the selected card didn't open it")
	}
}

// A tall narrow pane shows whole cards and says how many more there are.
func TestThePaneShowsWholeCards(t *testing.T) {
	m := demoModel(t, modePane, 62, 40)
	v := m.View()
	if !strings.Contains(v, "more") {
		t.Error("no word of the cards below")
	}
	// Every card drawn has its bottom border.
	if strings.Count(v, "╭") != strings.Count(v, "╰") {
		t.Error("a card cut off")
	}
}

// A screen taller than the room scrolls, saying what's above and below,
// and gets to its end.
func TestTheDetailsScroll(t *testing.T) {
	m := demoModel(t, modePane, 60, 24) // narrow and short: one column, stacked
	m.cursor, m.detail = 1, true
	press := func(k string) {
		next, _ := m.handleKey(keyMsg(k))
		m = next.(model)
	}
	if len(m.body()) <= m.bodyHeight() {
		t.Fatalf("the details fit in %d lines: nothing to scroll", m.bodyHeight())
	}
	v := m.View()
	if !strings.Contains(v, "more · pgdn") || strings.Contains(v, "more · pgup") {
		t.Fatalf("at the top: no word of what's below\n%s", v)
	}
	press("end")
	v = m.View()
	if strings.Contains(v, "more · pgdn") || !strings.Contains(v, "more · pgup") || !strings.Contains(v, "BUSIEST") {
		t.Errorf("at the end: not at the end\n%s", v)
	}
	press("home")
	press("down")
	if m.scrollY != 1 {
		t.Errorf("down scrolled to %d", m.scrollY)
	}
	next, _ := m.handleMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	if m = next.(model); m.scrollY != 4 {
		t.Errorf("the wheel scrolled to %d", m.scrollY)
	}
}

// On the Agents screen, ↑↓ go from session to session and the screen
// follows; pgdn scrolls.
func TestTheAgentsScreenFollowsTheSession(t *testing.T) {
	m := demoModel(t, modePopup, 120, 22)
	next, _ := m.openAI()
	m = next.(model)
	u := demoUsage(m.now)
	m.ai = &u
	for range len(u.Agents) - 1 {
		next, _ := m.handleKey(keyMsg("down"))
		m = next.(model)
	}
	if m.aiCursor != len(u.Agents)-1 {
		t.Fatalf("cursor %d", m.aiCursor)
	}
	if !strings.Contains(m.View(), "Port the CLI to Rust") {
		t.Errorf("the last session isn't on screen\n%s", m.View())
	}
	for range len(u.Agents) {
		next, _ := m.handleKey(keyMsg("up"))
		m = next.(model)
	}
	if m.scrollY != 0 && !strings.Contains(m.View(), "Billing webhooks") {
		t.Errorf("back at the first session, it isn't on screen")
	}
}
