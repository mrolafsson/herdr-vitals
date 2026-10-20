package main

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The Agents screen: every account the agents run on, how near it is to
// each of its limits and when they reset, and under it its agents, each
// with how fast it's using tokens now and how many it has so far.

type aiMsg aiUsage

// aiEvery is how often the agents are looked at: often while their screen
// is open, now and then for the header's summary.
func (m model) aiEvery() time.Duration {
	if m.aiView {
		return 3 * time.Second
	}
	return 30 * time.Second
}

// collectAI looks at the agents and accounts, off the UI's thread.
func (m model) collectAI() tea.Cmd {
	if m.demo {
		now := m.now
		return func() tea.Msg { return aiMsg(demoUsage(now)) }
	}
	c, ctx := m.col, m.ctx
	return func() tea.Msg { return aiMsg(c.collect(ctx, time.Now())) }
}

type aiTickMsg struct{}

func (m model) aiTick() tea.Cmd {
	return tea.Tick(m.aiEvery(), func(time.Time) tea.Msg { return aiTickMsg{} })
}

// aiAgents is the agents in the order the screen has them: by account, as
// the accounts are listed, then those on none.
func (m model) aiAgents() []agentRow {
	if m.ai == nil {
		return nil
	}
	var out []agentRow
	keys := map[string]bool{}
	for _, a := range m.ai.Accounts {
		keys[a.Key] = true
		for _, r := range m.ai.Agents {
			if r.Account == a.Key {
				out = append(out, r)
			}
		}
	}
	for _, r := range m.ai.Agents {
		if !keys[r.Account] {
			out = append(out, r)
		}
	}
	return out
}

// focusAgent goes to an agent's pane, and the popup closes.
func focusAgent(paneID string) error {
	return herdrCall("agent.focus", map[string]any{"target": paneID}, nil)
}

// aiKey is a key on the Agents screen, if it means something there.
func (m model) aiKey(k tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	rows := m.aiAgents()
	switch k.String() {
	case "esc", "a":
		m.aiView = false
		return m, nil, true
	case "z":
		m.aiDay = !m.aiDay
	case "up", "k":
		m.aiCursor = max(0, m.aiCursor-1)
		m.followAI()
	case "down", "j":
		m.aiCursor = min(max(0, len(rows)-1), m.aiCursor+1)
		m.followAI()
	case "enter":
		if m.aiCursor < len(rows) && !m.demo {
			pane := rows[m.aiCursor].Info.PaneID
			if focusAgent(pane) == nil && m.mode == modePopup {
				return m.quitWith()
			}
		}
	case "q", "ctrl+c":
		return m, nil, false
	default:
		return m, nil, false
	}
	return m, nil, true
}

func (m model) quitWith() (tea.Model, tea.Cmd, bool) {
	next, cmd := m.quit()
	return next, cmd, true
}

// aiSummary is the header's word on the accounts: the one nearest a limit,
// and how near ("tvh week 100%"). "" before they're known.
func (m model) aiSummary() string {
	if m.ai == nil {
		return ""
	}
	var top limitBar
	name := ""
	for _, a := range m.ai.Accounts {
		for _, l := range shownLimits(a.Limits) {
			if l.Percent > top.Percent || name == "" {
				top, name = l, a.Name
			}
		}
	}
	if name == "" {
		return ""
	}
	return styleDim.Render("nearest limit: "+name+" "+top.Label+" ") + valueStyle(limitLevel(top)).Render(pctText(top.Percent))
}

// hueUsage is the colour of tokens used, in every graph of them; limits
// are in the accent.
func hueUsage() lipgloss.TerminalColor { return look.Mauve }

// aiSpan is how far back the Agents screen's graphs go: three hours, or
// with z the day.
func (m model) aiSpan() time.Duration {
	if m.aiDay {
		return 24 * time.Hour
	}
	return 3 * time.Hour
}

func spanText(d time.Duration) string {
	if d >= 24*time.Hour {
		return "24h"
	}
	return itoa(int(d.Hours())) + "h"
}

// The graphs' gutters: their label to the left, their value to the right,
// so every graph on the screen shares one clock.
const aiGutter, aiValue = 7, 8

// viewAI is the Agents screen in height lines: each account's limits and
// usage as graphs on one clock, then every agent's session on it too.
func (m model) viewAI(height int) []string {
	w := m.width - 4
	if m.ai == nil {
		return indent([]string{styleDim.Render("Looking at the agents…")})
	}
	if len(m.ai.Accounts) == 0 && len(m.ai.Agents) == 0 {
		return indent([]string{styleDim.Render("No agents running, and no Claude or Codex login found.")})
	}
	gw := max(10, w-aiGutter-aiValue)
	// The graphs as tall as fit with every session under them.
	sessions := len(m.ai.Agents) + 3
	var top []string
	for _, gh := range []int{3, 2, 1} {
		top = append(top[:0], m.aiAxis(w, gw)...)
		for _, a := range m.ai.Accounts {
			top = append(top, m.accountBlock(a, w, gw, gh)...)
		}
		if len(top)+sessions <= height {
			break
		}
	}
	return indent(append(top, m.sessionLines(w)...))
}

// aiSelLine is the line the selected session is on, in viewAI's lines.
func (m model) aiSelLine(height int) int {
	lines := m.viewAI(height)
	return len(lines) - len(m.aiAgents()) + m.aiCursor
}

// aiAxis is the lines over the graphs: what they are, and when.
func (m model) aiAxis(w, gw int) []string {
	span := m.aiSpan()
	head := styleHeader.Render("ACCOUNTS") + styleDim.Render("  usage and limits, the last "+spanText(span)+", a column "+durText(span/time.Duration(gw)))
	axis := pad(strings.Repeat(" ", aiGutter)+styleDim.Render(spanText(span)+" ago"), aiGutter+gw-3) + styleDim.Render("now")
	return []string{fit(head, w), axis}
}

// durText is a short while, briefly: "1m", "5m", "1h".
func durText(d time.Duration) string {
	switch {
	case d >= time.Hour:
		return itoa(int(d.Hours()+0.5)) + "h"
	case d >= time.Minute:
		return itoa(int(d.Minutes()+0.5)) + "m"
	}
	return itoa(max(1, int(d.Seconds()+0.5))) + "s"
}

// accountBlock is an account: its name and its limits as gauges; then its
// usage, all its agents' tokens a minute, as a graph; and under it, on the
// same clock, how full each limit was.
func (m model) accountBlock(a account, w, gw, gh int) []string {
	lines := []string{""}
	name := styleTitle.Render(a.Name)
	if a.Plan != "" {
		name += styleDim.Render("  " + a.Plan)
	}
	right := styleDim.Render("as of " + agoText(a.At, m.now))
	switch {
	case a.Err != "":
		right = styleWarn.Render(fit(a.Err, max(10, w/2)))
	case a.At.IsZero():
		right = ""
	}
	if a.Credits != "" {
		right = styleDim.Render(a.Credits+" · ") + right
	}
	lines = append(lines, spread(name, right, w))
	lines = append(lines, limitGauges(shownLimits(a.Limits), w, m.now)...)

	span := m.aiSpan()
	var rows []agentRow
	for _, r := range m.ai.Agents {
		if r.Account == a.Key {
			rows = append(rows, r)
		}
	}
	use := usageColumns(sumMinutes(rows), gw, span)
	rate := 0.0
	for _, r := range rows {
		rate += r.Rate
	}
	if len(rows) > 0 {
		topV, clipped := usageTop(use)
		g := graph(use, gw, gh, topV, hueUsage(), calm)
		for i, l := range g {
			label, value := "", ""
			if i == 0 {
				label = "tokens"
				over := ""
				if clipped {
					over = "≥" // and more, off the top
				}
				value = styleDim.Render(over + countText(int(topV)) + "/m")
			}
			if i == len(g)-1 {
				label = "a min"
				value = valueOrDash(rate)
			}
			lines = append(lines, styleDim.Render(pad(label, aiGutter))+l+" "+lipgloss.PlaceHorizontal(aiValue-1, lipgloss.Right, value))
		}
	}
	// How full each limit was, under the usage it came from.
	for _, l := range shownLimits(a.Limits) {
		cols := limitColumns(m.ai.History[a.Key], l.Label, gw, span, m.now)
		if seriesMax(cols) < 0 {
			continue // nothing written down yet
		}
		lv := limitLevel(l)
		lines = append(lines, styleDim.Render(pad(fit(l.Label, aiGutter-1), aiGutter))+
			spark(cols, gw, 100, look.Accent, func(v float64) level { return limitLevel(limitBar{Percent: v}) })+" "+
			lipgloss.PlaceHorizontal(aiValue-1, lipgloss.Right, valueStyle(lv).Render(pctText(l.Percent))))
	}
	return lines
}

func valueOrDash(rate float64) string {
	if rate < 1 {
		return styleDim.Render("–")
	}
	return styleLead.Render(countText(int(rate)))
}

// limitGauges is an account's limits as gauges, as many to a line as fit:
// how full, and when it resets.
func limitGauges(ls []limitBar, w int, now time.Time) []string {
	if len(ls) == 0 {
		return nil
	}
	const label, rest, gap = 6, 5 + 1 + 13, 3 // "week  ", " 100%", " resets Sat 01:00"
	per := len(ls)
	gw := 0
	for ; per > 1; per-- {
		if gw = (w-(per-1)*gap)/per - label - rest; gw >= 10 {
			break
		}
	}
	if per == 1 {
		gw = max(6, min(40, w-label-rest))
	}
	gw = min(gw, 40)
	var lines []string
	var cur []string
	for i, l := range ls {
		lv := limitLevel(l)
		item := styleDim.Render(pad(fit(l.Label, label-1), label)) + gauge(l.Percent, gw, levelColor(look.Accent, lv)) + " " +
			lipgloss.PlaceHorizontal(4, lipgloss.Right, valueStyle(lv).Render(pctText(l.Percent)))
		if r := resetText(l.ResetsAt, now); r != "" {
			item += styleDim.Render(" ↻ " + r)
		}
		cur = append(cur, pad(item, label+gw+rest))
		if len(cur) == per || i == len(ls)-1 {
			lines = append(lines, fit(strings.Join(cur, strings.Repeat(" ", gap)), w))
			cur = nil
		}
	}
	return lines
}

// sessionLines is every agent's session: its account, state, title, model,
// its tokens a minute on the graphs' clock, how fast now and how many so far.
func (m model) sessionLines(w int) []string {
	lines := []string{"", spread(styleHeader.Render("SESSIONS"), m.aiColumns(w), w)}
	for i, r := range m.aiAgents() {
		lines = append(lines, m.agentLine(r, w, i == m.aiCursor))
	}
	return lines
}

func (m model) accountName(key string) string {
	for _, a := range m.ai.Accounts {
		if a.Key == key {
			return a.Name
		}
	}
	return ""
}

func (m model) sessionSparkW(w int) int {
	if w < 90 {
		return 8
	}
	return 24
}

// agentLine is an agent's session: its state, its title and where it is,
// its account, its model, its tokens a minute on the graphs' clock, how
// fast now and how many so far.
func (m model) agentLine(r agentRow, w int, selected bool) string {
	sparkW := m.sessionSparkW(w)
	cols := usageColumns(r.Minutes, sparkW, m.aiSpan())
	// Each graph to its own busy moments: one agent's burst would flatten
	// everyone else's. How fast against the others is the number beside it.
	topV, _ := usageTop(cols)
	right := styleDim.Render(pad(fit(m.accountName(r.Account), 8), 8)) + " " +
		styleDim.Render(pad(fit(modelText(r.Model), 10), 10)) + " " + spark(cols, sparkW, topV, hueUsage(), calm) + " " +
		lipgloss.PlaceHorizontal(6, lipgloss.Right, valueOrDash(r.Rate)) + " " +
		lipgloss.PlaceHorizontal(6, lipgloss.Right, styleText.Render(countText(int(r.Work))))
	lead, title := "  ", styleLead.Render(r.Info.title())
	if selected {
		lead, title = styleTree.Render("▸ "), styleTree.Bold(true).Render(r.Info.title())
	}
	left := lead + agentMark(r.Info.Status, "") + " " + title
	if r.Space != "" {
		left += styleDim.Render(" · " + r.Space)
	}
	return spread(left, right, w)
}

// aiColumns names the sessions' columns, over them at the right.
func (m model) aiColumns(w int) string {
	sparkW := m.sessionSparkW(w)
	return styleDim.Render(pad("account", 8) + " " + pad("model", 10) + " " + pad("last "+spanText(m.aiSpan()), sparkW) + " " +
		lipgloss.PlaceHorizontal(6, lipgloss.Right, "a min") + " " + lipgloss.PlaceHorizontal(6, lipgloss.Right, "tokens"))
}

// ── on the machines' screen ───────────────────────────────────────────────────

// aiIdx is the AI card's place among the cards: after the machines.
func (m model) aiIdx() int { return len(m.hosts) }

// hasAICard is whether the cards have one for the AI accounts: once they're
// known, among several machines (one machine's screen is its details,
// which have them too: aiCompact).
func (m model) hasAICard() bool {
	if m.ai == nil || len(m.ai.Accounts) == 0 || m.single() {
		return false
	}
	// Only where it fits: every card, it too, without scrolling.
	n := len(m.hosts) + 1
	l := m.gridFor(n)
	used, rowH := 0, 0
	for i := 0; i < n; i++ {
		h := len(m.aiCardLines(l.w-4)) + 2
		if i < len(m.hosts) {
			h = len(m.cardLines(m.hosts[i], l.w-4)) + 2
		}
		rowH = max(rowH, h)
		if (i+1)%l.cols == 0 || i == n-1 {
			used, rowH = used+rowH, 0
		}
	}
	return used <= m.bodyHeight()
}

// shownLimits is the limits worth showing: the session (five hours), the
// week, and any for one model (Fable); in that order. Anthropic's list has
// others (for a surface, say) that a glance can do without.
func shownLimits(ls []limitBar) []limitBar {
	var out []limitBar
	for _, want := range []string{"session", "week"} {
		for _, l := range ls {
			if l.Label == want {
				out = append(out, l)
			}
		}
	}
	for _, l := range ls {
		if l.Model {
			out = append(out, l)
		}
	}
	return out
}

// mainLimits is the limits a card has room for: the session and the week,
// else the first two shown.
func mainLimits(ls []limitBar) []limitBar {
	shown := shownLimits(ls)
	var out []limitBar
	for _, l := range shown {
		if l.Label == "session" || l.Label == "week" {
			out = append(out, l)
		}
	}
	if len(out) == 0 && len(shown) > 0 {
		out = shown[:min(2, len(shown))]
	}
	return out
}

// accountUsage is an account's agents' tokens a minute over the last hour,
// and how fast now.
func (m model) accountUsage(key string, width int) ([]float64, float64) {
	var rows []agentRow
	rate := 0.0
	for _, r := range m.ai.Agents {
		if r.Account == key {
			rows = append(rows, r)
			rate += r.Rate
		}
	}
	return usageColumns(sumMinutes(rows), width, time.Hour), rate
}

// smallGauge is a limit in little: its label, a gauge and how full.
func smallGauge(l limitBar, gw int) string {
	lv := limitLevel(l)
	label := map[string]string{"session": "sess", "week": "wk"}[l.Label]
	if label == "" {
		label = l.Label
	}
	return styleDim.Render(pad(fit(label, 5), 5)) + gauge(l.Percent, gw, levelColor(look.Accent, lv)) + " " +
		lipgloss.PlaceHorizontal(4, lipgloss.Right, valueStyle(lv).Render(pctText(l.Percent)))
}

// aiLevel is the most any account's limit calls for, and which.
func (m model) aiLevel() (level, string) {
	worst, why := levelOK, ""
	for _, a := range m.ai.Accounts {
		for _, l := range shownLimits(a.Limits) {
			if lv := limitLevel(l); lv > worst {
				worst, why = lv, a.Name+" "+l.Label
			}
		}
	}
	return worst, why
}

// aiCardLines is the AI card inside its border, w wide: each account's
// usage over the last hour, and its five hours and week.
func (m model) aiCardLines(w int) []string {
	mark := styleOK.Render("●")
	switch lv, why := m.aiLevel(); lv {
	case levelHot:
		mark = statePill(styleErr, "HOT "+why)
	case levelWarn:
		mark = statePill(styleWarn, "WARN "+why)
	}
	n := len(m.ai.Accounts)
	lines := []string{spread(styleTitle.Render("AI")+styleDim.Render(" · "+itoa(n)+" account"+map[bool]string{true: "s", false: ""}[n != 1]), mark, w),
		styleDim.Render(fit(itoa(len(m.ai.Agents))+" agents · usage, the last hour", w))}
	sw := max(6, min(16, w/3))
	for _, a := range m.ai.Accounts {
		use, rate := m.accountUsage(a.Key, sw)
		right := spark(use, sw, first(usageTop(use)), hueUsage(), calm) + " " + lipgloss.PlaceHorizontal(5, lipgloss.Right, valueOrDash(rate))
		name := styleLead.Render(a.Name)
		if a.Plan != "" {
			name += styleDim.Render(" " + a.Plan)
		}
		lines = append(lines, "", spread(name, right, w))
		ls := shownLimits(a.Limits)
		if len(ls) == 0 {
			msg := "no limits yet"
			if a.Err != "" {
				msg = a.Err
			}
			lines = append(lines, styleDim.Render(fit(msg, w)))
			continue
		}
		gw := max(3, (w-(len(ls)-1)*2)/len(ls)-10)
		var parts []string
		for _, l := range ls {
			parts = append(parts, smallGauge(l, gw))
		}
		lines = append(lines, fit(strings.Join(parts, "  "), w))
	}
	return lines
}

// aiCard is the AI card, border and all, as tall as height.
func (m model) aiCard(w, height int) []string {
	inner := m.aiCardLines(w - 4)
	for len(inner) < height-2 {
		inner = append(inner, "")
	}
	border := look.Surface
	switch lv, _ := m.aiLevel(); {
	case m.cursor == m.aiIdx():
		border = look.Accent
	case lv == levelHot:
		border = look.Red
	case lv == levelWarn:
		border = look.Yellow
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1).Width(w - 2)
	return strings.Split(box.Render(strings.Join(inner, "\n")), "\n")
}

// aiCompact is the AI accounts in a machine's details, when it's the only
// machine: a line each, its five hours and week and its usage this hour.
func (m model) aiCompact(w int) []string {
	if m.ai == nil || len(m.ai.Accounts) == 0 {
		return nil
	}
	lines := []string{styleHeader.Render("AI") + styleDim.Render("  limits, and usage the last hour · a for more")}
	nameW := 8
	for _, a := range m.ai.Accounts {
		nameW = max(nameW, min(14, lipgloss.Width(a.Name)+2))
	}
	// Kept together, left to right: the account, its limits, its usage; or
	// narrow, the account and its usage over its limits.
	const gw, gap = 12, "   "
	limitsW := 3*(5+gw+5) + 2*len(gap) // session, week and a model's
	wide := w >= nameW+limitsW+len(gap)+8+6
	sw := max(8, min(30, w-nameW-limitsW-len(gap)-6))
	if !wide {
		sw = max(6, min(20, w-nameW-6))
	}
	for _, a := range m.ai.Accounts {
		use, rate := m.accountUsage(a.Key, sw)
		usage := spark(use, sw, first(usageTop(use)), hueUsage(), calm) + " " + lipgloss.PlaceHorizontal(5, lipgloss.Right, valueOrDash(rate))
		ls := mainLimits(a.Limits)
		name := styleLead.Render(pad(fit(a.Name, nameW-1), nameW))
		if !wide {
			// Narrow: the account and its usage, then a line a limit, with
			// room for a gauge you can read and when it resets.
			lines = append(lines, spread(name, usage, w))
			if len(ls) == 0 {
				lines = append(lines, "  "+styleDim.Render(fit(a.Err, w-2)))
				continue
			}
			for _, l := range shownLimits(a.Limits) {
				lv := limitLevel(l)
				reset := ""
				if r := resetText(l.ResetsAt, m.now); r != "" {
					reset = styleDim.Render("  ↻ " + r)
				}
				// A column for when it resets, so the gauges line up.
				const rw = 13 // "  ↻ Mon 18:00"
				lgw := max(6, w-2-9-5-rw)
				lines = append(lines, fit("  "+styleDim.Render(pad(fit(l.Label, 8), 9))+gauge(l.Percent, lgw, levelColor(look.Accent, lv))+" "+
					lipgloss.PlaceHorizontal(4, lipgloss.Right, valueStyle(lv).Render(pctText(l.Percent)))+reset, w))
			}
			continue
		}
		var parts []string
		for _, l := range shownLimits(a.Limits) {
			parts = append(parts, smallGauge(l, gw))
		}
		mid := pad(strings.Join(parts, gap), limitsW)
		if len(parts) == 0 {
			mid = styleDim.Render(pad(fit(a.Err, limitsW), limitsW))
		}
		lines = append(lines, fit(name+mid+gap+usage, w))
	}
	return lines
}

func first(v float64, _ bool) float64 { return v }

// followAI scrolls the Agents screen so the selected session's on it.
func (m *model) followAI() {
	h := m.bodyHeight()
	sel := m.aiSelLine(h)
	switch {
	case sel < m.scrollY:
		m.scrollY = sel
	case sel >= m.scrollY+h-1: // the last line may be "↓ more"
		m.scrollY = sel - h + 2
	}
}

// aiWeeks is the AI accounts at their least: each its week, a line each,
// with when it resets (the session or a model's, for one with no week).
func (m model) aiWeeks(w int) []string {
	if m.ai == nil || len(m.ai.Accounts) == 0 {
		return nil
	}
	lines := []string{styleHeader.Render("AI") + styleDim.Render("  each account's week · a for more")}
	nameW := 8
	for _, a := range m.ai.Accounts {
		nameW = max(nameW, min(14, lipgloss.Width(a.Name)+2))
	}
	const rw = 13 // "  ↻ Mon 18:00": a column, so the gauges line up
	for _, a := range m.ai.Accounts {
		name := styleLead.Render(pad(fit(a.Name, nameW-1), nameW))
		ls := shownLimits(a.Limits)
		var l *limitBar
		for i := range ls {
			if ls[i].Label == "week" {
				l = &ls[i]
			}
		}
		if l == nil && len(ls) > 0 {
			l = &ls[0]
		}
		if l == nil {
			lines = append(lines, name+styleDim.Render(fit(a.Err, w-nameW)))
			continue
		}
		lv := limitLevel(*l)
		label := ""
		if l.Label != "week" {
			label = styleDim.Render(l.Label + " ")
		}
		reset := ""
		if r := resetText(l.ResetsAt, m.now); r != "" {
			reset = styleDim.Render("  ↻ " + r)
		}
		gw := max(6, w-nameW-lipgloss.Width(label)-5-rw)
		lines = append(lines, fit(name+label+gauge(l.Percent, gw, levelColor(look.Accent, lv))+" "+
			lipgloss.PlaceHorizontal(4, lipgloss.Right, valueStyle(lv).Render(pctText(l.Percent)))+reset, w))
	}
	return lines
}
