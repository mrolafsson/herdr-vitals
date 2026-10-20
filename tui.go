package main

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// mode is where the monitor is shown: a popup you glance at and close, or a
// pane that stays.
type mode int

const (
	modePopup mode = iota
	modePane
)

// hostState is everything known about one machine.
type hostState struct {
	t      target
	status string // "connecting", "live", "down", "auth"
	err    string
	retry  time.Time
	last   *sample
	v      vitals
	hist   history
	gpu    ring // the first GPU's use, %
	level  level
	why    string
	dlog   *diskLog
	fills  map[string]time.Duration // filesystem → until it's full
}

// up is whether the machine is answering now.
func (h *hostState) up() bool { return h.status == "live" && h.last != nil }

type model struct {
	ctx        context.Context
	cancel     context.CancelFunc // stops the watchers, for a reconnect
	cfg        config
	mode       mode
	demo       bool
	hosts      []*hostState
	frames     chan frameMsg
	cursor     int // the selected machine: an index into hosts
	detail     bool
	procs      bool // the selected machine's processes, the whole screen
	byMem      bool // the processes by memory, not CPU
	procScroll int
	scrollY    int  // the details' or the Agents screen's first line shown
	hottest    bool // sorted hottest first, not as listed
	scroll     int  // the first row of cards shown
	width      int
	height     int
	mouseX     int
	mouseY     int
	now        time.Time
	beat       bool // flips with each round, for the live dot
	openPane   bool // asked for the pane: open it once the popup has gone
	// day: the details' graphs over the last day, a point a minute, from
	// what the sampler kept (dayOf, by machine, read when asked for).
	day      bool
	dayOf    map[int][]point
	dayRead  time.Time
	sampling bool // the sampler's running: it writes the disks down
	// The agents and their accounts (aiview.go): looked at now and then,
	// often while their screen (aiView) is open.
	ai         *aiUsage
	aiView     bool
	aiDay      bool // its graphs over the day, not three hours
	aiCursor   int
	col        *collector
	collecting bool
}

type tickMsg time.Time

func newModel(ctx context.Context, cfg config, m mode, demo bool) model {
	var ts []target
	if demo {
		ts = demoTargets()
	} else {
		ts = targets(cfg)
	}
	hosts := make([]*hostState, len(ts))
	now := time.Now()
	for i, t := range ts {
		hosts[i] = &hostState{t: t, status: "connecting"}
		// Graphs full from the start, from what the sampler kept.
		if !demo {
			hosts[i].hist.seed(freshRecent(t, cfg.interval(), now))
		}
	}
	md := model{ctx: ctx, cfg: cfg, mode: m, demo: demo, hosts: hosts,
		frames: make(chan frameMsg, 256), now: now, sampling: !demo && samplerRunning(), col: newCollector()}
	// One machine is all there is to see: its details, not a lonely card.
	md.detail = len(hosts) == 1
	md.start()
	return md
}

// single is whether there's only the one machine: its details are the
// monitor, with nothing to go back to.
func (m model) single() bool { return len(m.hosts) == 1 }

func (m model) Init() tea.Cmd {
	return tea.Batch(listen(m.frames), tick(), func() tea.Msg { return aiTickMsg{} })
}

// start sets every machine's watcher going.
func (m *model) start() {
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	for i, h := range m.hosts {
		if m.demo {
			go demoWatch(ctx, i, h.t, m.cfg.interval(), m.frames)
		} else {
			go watch(ctx, i, h.t, m.cfg.interval(), false, m.frames)
		}
	}
}

func listen(ch chan frameMsg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.keepInView()
		return m, nil
	case tickMsg:
		m.now = time.Time(msg)
		if m.day && m.now.Sub(m.dayRead) >= time.Minute {
			m.readDay() // a point a minute: as often as there's a new one
		}
		return m, tick()
	case frameMsg:
		m.apply(msg)
		return m, listen(m.frames)
	case aiTickMsg:
		if m.collecting {
			return m, m.aiTick()
		}
		m.collecting = true
		return m, m.collectAI()
	case aiMsg:
		u := aiUsage(msg)
		m.ai, m.collecting = &u, false
		if m.cursor >= len(m.hosts) && !m.hasAICard() {
			m.cursor = 0 // the AI card's gone
		}
		m.aiCursor = min(m.aiCursor, max(0, len(m.aiAgents())-1))
		return m, m.aiTick()
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.MouseMsg:
		return m.handleMouse(msg)
	}
	return m, nil
}

// apply takes in news of a machine.
func (m *model) apply(f frameMsg) {
	if f.host < 0 || f.host >= len(m.hosts) {
		return
	}
	h := m.hosts[f.host]
	if f.sample == nil {
		h.status, h.err, h.retry = f.status, f.err, f.retry
		return
	}
	prev := h.last
	if prev != nil && f.sample.At.Sub(prev.At) > 3*m.cfg.interval()+10*time.Second {
		prev = nil // the last round was before a gap: no rate across it
	}
	h.v = derive(prev, f.sample)
	// The first round of a Linux machine has no rates yet: nothing to graph.
	if prev != nil || f.sample.OS == "darwin" {
		h.hist.record(h.v)
		if len(f.sample.GPUs) > 0 {
			h.gpu.push(f.sample.GPUs[0].Util)
		}
	}
	h.last, h.status, h.err = f.sample, "live", ""
	if m.demo {
		h.fills = demoFills[f.host]
	} else {
		if h.dlog == nil {
			h.dlog = openDiskLog(h.t)
		}
		if !m.sampling { // else the sampler writes them down
			h.dlog.note(f.sample)
		}
		h.fills = h.dlog.fills(f.sample.At)
	}
	h.level, h.why = health(f.sample, h.v, &h.hist, h.fills)
	if f.sample.At.After(m.now) {
		m.now = f.sample.At
	}
	m.beat = !m.beat
}

// order is the machines in the order they're shown: as listed (this one,
// herdr's, the config's), or the ones that want you first.
func (m model) order() []int {
	out := make([]int, len(m.hosts))
	for i := range out {
		out[i] = i
	}
	if m.hottest {
		rank := func(h *hostState) int {
			switch {
			case h.level == levelHot && h.up():
				return 0
			case h.level == levelWarn && h.up():
				return 1
			case h.status == "auth" || h.status == "down":
				return 2
			}
			return 3
		}
		sort.SliceStable(out, func(a, b int) bool {
			ha, hb := m.hosts[out[a]], m.hosts[out[b]]
			if ra, rb := rank(ha), rank(hb); ra != rb {
				return ra < rb
			}
			return ha.hist.CPU.avg(3) > hb.hist.CPU.avg(3)
		})
	}
	if m.hasAICard() {
		out = append(out, m.aiIdx())
	}
	return out
}

// position is where the selected machine is in the order shown.
func (m model) position() int {
	for i, h := range m.order() {
		if h == m.cursor {
			return i
		}
	}
	return 0
}

// moveBy selects the machine n places on in the order shown.
func (m *model) moveBy(n int) {
	m.scrollY = 0
	o := m.order()
	if len(o) == 0 {
		return
	}
	p := max(0, min(len(o)-1, m.position()+n))
	m.cursor = o[p]
	m.keepInView()
}

func (m model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	cols := m.grid().cols
	if m.procs {
		if next, cmd, ok := m.procKey(k); ok {
			return next, cmd
		}
	}
	if m.scrolls() {
		if next, ok := m.scrollKey(k); ok {
			return next, nil
		}
	}
	if m.aiView {
		if next, cmd, ok := m.aiKey(k); ok {
			return next, cmd
		}
	}
	switch k.String() {
	case "ctrl+c", "q":
		return m.quit()
	case "t":
		if m.cursor < len(m.hosts) {
			m.procs, m.procScroll = true, 0
		}
	case "a":
		return m.openAI()
	case "z":
		if m.detail {
			m.day = !m.day
			if m.day {
				m.readDay()
			}
		}
	case "esc":
		if m.detail && !m.single() {
			m.detail = false
			m.keepInView()
			return m, nil
		}
		if m.mode == modePopup {
			return m.quit()
		}
	case "enter", " ":
		if m.cursor == m.aiIdx() && m.hasAICard() {
			return m.openAI()
		}
		if len(m.hosts) > 1 {
			m.detail, m.scrollY = !m.detail, 0
			m.keepInView()
		}
	case "left", "h", "shift+tab":
		m.moveBy(-1)
	case "right", "l", "tab":
		m.moveBy(1)
	case "up", "k":
		if !m.detail {
			m.moveBy(-cols)
		}
	case "down", "j":
		if !m.detail {
			m.moveBy(cols)
		}
	case "s":
		m.hottest = !m.hottest
		m.keepInView()
	case "r":
		// Every machine afresh: a host whose ssh you've just put right needn't
		// wait out its retry.
		if m.cancel != nil {
			m.cancel()
		}
		for _, h := range m.hosts {
			if !h.up() {
				h.status, h.err = "connecting", ""
			}
		}
		m.start()
	case "p":
		if m.mode == modePopup && !m.demo {
			m.openPane = true
			return m.quit()
		}
	}
	return m, nil
}

// procKey is a key on the processes screen, if it means something there:
// up and down scroll, m sorts by memory or CPU, esc goes back.
func (m model) procKey(k tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	page := max(1, m.height-8)
	switch k.String() {
	case "esc", "t":
		m.procs = false
	case "m", "c":
		m.byMem, m.procScroll = !m.byMem, 0
	case "up", "k":
		m.procScroll = max(0, m.procScroll-1)
	case "down", "j":
		m.procScroll++
	case "pgup":
		m.procScroll = max(0, m.procScroll-page)
	case "pgdown", " ":
		m.procScroll += page
	case "home", "g":
		m.procScroll = 0
	case "left", "h", "shift+tab", "right", "l", "tab":
		m.procScroll = 0
		return m, nil, false // the next machine's, as anywhere
	default:
		return m, nil, false
	}
	if hs := m.hosts; m.cursor < len(hs) {
		m.procScroll = min(m.procScroll, max(0, len(hs[m.cursor].v.Top)-1))
	}
	return m, nil, true
}

// readDay reads every machine's day, as the sampler has kept it (the demo
// makes its own up).
func (m *model) readDay() {
	m.dayOf, m.dayRead = map[int][]point{}, m.now
	for i, h := range m.hosts {
		if m.demo {
			m.dayOf[i] = demoDay(i, m.now)
		} else {
			m.dayOf[i] = readPoints(h.t, "day")
		}
	}
}

// openAI shows the Agents screen, looking at them afresh.
func (m model) openAI() (tea.Model, tea.Cmd) {
	m.aiView, m.aiCursor, m.scrollY = true, 0, 0
	if !m.collecting {
		m.collecting = true
		return m, m.collectAI()
	}
	return m, nil
}

func (m model) quit() (tea.Model, tea.Cmd) {
	if m.cancel != nil {
		m.cancel()
	}
	return m, tea.Quit
}

// handleMouse: a click selects a machine, and on the selected one opens it;
// the wheel moves along; the keys along the bottom are buttons.
func (m model) handleMouse(ev tea.MouseMsg) (tea.Model, tea.Cmd) {
	m.mouseX, m.mouseY = ev.X, ev.Y
	if ev.Action != tea.MouseActionPress {
		return m, nil
	}
	switch ev.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		n := 1
		if ev.Button == tea.MouseButtonWheelUp {
			n = -1
		}
		if m.scrolls() {
			m.scrollBy(3 * n) // the details and the Agents screen scroll
		} else {
			m.moveBy(n) // the cards move along
		}
		return m, nil
	case tea.MouseButtonLeft:
	default:
		return m, nil
	}
	if ev.Y == m.height-1 {
		if k := hintAt(m.footer(), ev.X, m.width); k != "" {
			return m.handleKey(keyMsg(k))
		}
		return m, nil
	}
	if m.detail || m.procs || m.aiView || m.strip() {
		return m, nil
	}
	if i, ok := m.cardAt(ev.X, ev.Y); ok {
		if i == m.cursor && i == m.aiIdx() {
			return m.openAI()
		}
		if i == m.cursor {
			m.detail = true
		}
		m.cursor = i
		m.keepInView()
	}
	return m, nil
}

// footer is the keys along the bottom, for where you are.
func (m model) footer() []hint {
	var hs []hint
	if m.aiView {
		span := "z last day"
		if m.aiDay {
			span = "z last 3h"
		}
		hs = append(hs, hint{"esc back", "esc", hintQuiet}, hint{span, "z", hintView}, hint{"↑↓ session", "down", hintView})
		if len(m.body()) > m.bodyHeight() {
			hs = append(hs, hint{"pgdn more", "pgdown", hintView})
		}
		if !m.demo {
			hs = append(hs, hint{"enter go to it", "enter", hintGo})
		}
		return hs
	}
	if m.procs {
		sortBy := "m by memory"
		if m.byMem {
			sortBy = "m by cpu"
		}
		hs = append(hs, hint{"esc back", "esc", hintQuiet}, hint{sortBy, "m", hintView}, hint{"↑↓ scroll", "down", hintView})
		if len(m.hosts) > 1 {
			hs = append(hs, hint{"← → machines", "right", hintView})
		}
		return hs
	}
	if m.detail {
		span := "z last day"
		if m.day {
			span = "z last minutes"
		}
		if !m.single() {
			hs = append(hs, hint{"esc back", "esc", hintQuiet})
		}
		hs = append(hs, hint{span, "z", hintView})
		if !m.single() {
			hs = append(hs, hint{"← → machines", "right", hintView})
		}
		if len(m.body()) > m.bodyHeight() {
			hs = append(hs, hint{"↑↓ scroll", "down", hintView})
		}
	} else if len(m.hosts) > 1 {
		hs = append(hs, hint{"enter details", "enter", hintGo})
		if len(m.hosts) > 1 {
			label := "s hottest first"
			if m.hottest {
				label = "s as listed"
			}
			hs = append(hs, hint{label, "s", hintView})
		}
	}
	if len(m.hosts) > 0 {
		hs = append(hs, hint{"t processes", "t", hintGo})
	}
	hs = append(hs, hint{"a agents", "a", hintGo})
	for _, h := range m.hosts {
		if !h.up() && h.status != "connecting" {
			hs = append(hs, hint{"r reconnect", "r", hintAct})
			break
		}
	}
	if m.mode == modePopup && !m.demo {
		hs = append(hs, hint{"p keep in a pane", "p", hintAct})
	}
	switch {
	case m.mode == modePopup && (!m.detail || m.single()):
		label := "esc close"
		if _, back := cameFrom(); back {
			label = "esc back"
		}
		hs = append(hs, hint{label, "esc", hintQuiet})
	case m.mode == modePane:
		hs = append(hs, hint{"q close", "q", hintQuiet})
	}
	return hs
}

// runUI shows the monitor until it's closed: the popup, or the pane.
func runUI(ctx context.Context, cfg config, md mode, demo bool) error {
	// Ask the terminal for its background now: once the program owns stdin,
	// the reply would arrive as stray input.
	if cfg.Theme != "dark" && cfg.Theme != "light" {
		cfg.Theme = "light"
		if lipgloss.HasDarkBackground() {
			cfg.Theme = "dark"
		}
	}
	darkTerminal = cfg.Theme == "dark"
	useTheme(pickerTheme(darkTerminal))
	switch {
	case md == modePopup && !demo:
		defer notePopup()()
	}
	m := newModel(ctx, cfg, md, demo)
	// WithContext: a SIGTERM (a newer open replacing this popup) ends it
	// cleanly, restoring the terminal.
	final, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithContext(ctx)).Run()
	if errors.Is(err, context.Canceled) || errors.Is(err, tea.ErrProgramKilled) {
		return nil
	}
	if err != nil {
		return err
	}
	// The pane opens beside the one the popup was over, once the popup has
	// gone: herdr has one popup at a time, and the pane is no popup.
	if fm, ok := final.(model); ok && fm.openPane {
		if err := openPane("pane", nil); err != nil {
			notify("Vitals", "Couldn't open the monitor: "+err.Error())
		}
		return nil
	}
	// Opened from another plugin's popup (herdr-recap's): back to it.
	if p, ok := cameFrom(); ok && md == modePopup {
		pendingSwap = &p
	}
	return nil
}

// strip is whether there's only room for a line or two: then every machine
// is a name, its load and a small graph of it, side by side.
func (m model) strip() bool {
	return m.height > 0 && m.height <= 6
}

func (m model) View() string {
	return screenSafe(m.view())
}

func (m model) view() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	if m.strip() {
		return m.viewStrip()
	}
	var body []string
	if len(m.hosts) == 0 {
		body = []string{"", styleDim.Render("  No machines to watch. Add some to config.json (see the README).")}
	} else if m.aiView {
		body = m.window(m.viewAI(m.height-3), m.height-3)
	} else if m.procs {
		body = m.viewProcs(m.hosts[m.cursor], m.height-3)
	} else if m.detail {
		body = m.window(m.viewDetail(m.hosts[m.cursor], m.height-3), m.height-3)
	} else {
		body = m.viewGrid(m.height - 3)
	}
	h := m.height - 3
	if len(body) > h {
		body = body[:h]
	}
	for len(body) < h {
		body = append(body, "")
	}
	return m.viewHeader() + "\n\n" + strings.Join(body, "\n") + "\n" + m.renderFooter(m.footer())
}

// renderFooter draws the keys along the bottom, the one under the pointer
// lit so it reads as clickable.
func (m model) renderFooter(hs []hint) string {
	hot := ""
	if m.mouseY == m.height-1 {
		hot = hintAt(hs, m.mouseX, m.width)
	}
	return footerLine(hs, hot, m.width)
}
