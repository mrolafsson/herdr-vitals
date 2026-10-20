package main

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// How the vitals are coloured. Each kind of reading has its own hue, so a
// graph is known by its colour before its label is read: CPU blue, load
// teal, memory mauve, disks (how full, and their traffic) green, the
// network peach. A
// reading that wants a look turns yellow, one that wants you now red: in a
// graph, only the moments that did, so a spike stands out of its history.
// What's empty is drawn on the quiet surface colour.

func hueCPU() lipgloss.TerminalColor  { return look.Blue }
func hueLoad() lipgloss.TerminalColor { return look.Teal }
func hueMem() lipgloss.TerminalColor  { return look.Mauve }
func hueDisk() lipgloss.TerminalColor { return look.Green }
func hueNet() lipgloss.TerminalColor  { return look.Peach }
func hueGPU() lipgloss.TerminalColor  { return look.Green }

// levelColor is a reading's colour: its hue while calm.
func levelColor(hue lipgloss.TerminalColor, l level) lipgloss.TerminalColor {
	switch l {
	case levelHot:
		return look.Red
	case levelWarn:
		return look.Yellow
	}
	return hue
}

func fg(c lipgloss.TerminalColor) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }

// readingStyle is a reading's style where its graph is beside it: the
// graph's colour while calm, its level's when not.
func readingStyle(hue lipgloss.TerminalColor, l level) lipgloss.Style {
	return fg(levelColor(hue, l)).Bold(true)
}

// valueStyle is a number's style: plain text while calm, its level's colour
// when not.
func valueStyle(l level) lipgloss.Style {
	if l == levelOK {
		return styleLead
	}
	return fg(levelColor(nil, l)).Bold(true)
}

// ── marks ─────────────────────────────────────────────────────────────────────

var eighths = []rune("▁▂▃▄▅▆▇█")

// runs draws cells of one colour together: far fewer escapes than a colour
// a cell.
type runs struct {
	b     strings.Builder
	color lipgloss.TerminalColor
	faint bool
	under lipgloss.TerminalColor // the cells' background, or nil
	cur   strings.Builder
}

func (r *runs) add(c lipgloss.TerminalColor, s string) { r.addCell(c, false, nil, s) }

// addFaint adds cells drawn faintly, or not.
func (r *runs) addFaint(c lipgloss.TerminalColor, faint bool, s string) { r.addCell(c, faint, nil, s) }

// addCell adds cells in c (faintly, or not) over a background, or none.
func (r *runs) addCell(c lipgloss.TerminalColor, faint bool, under lipgloss.TerminalColor, s string) {
	if r.cur.Len() > 0 && (c != r.color || faint != r.faint || under != r.under) {
		r.flush()
	}
	r.color, r.faint, r.under = c, faint, under
	r.cur.WriteString(s)
}

func (r *runs) flush() {
	if r.cur.Len() > 0 {
		style := fg(r.color)
		if r.faint {
			if c, ok := soften(r.color); ok {
				style = fg(c)
			} else {
				style = style.Faint(true) // a terminal colour: the terminal dims it
			}
		}
		if r.under != nil {
			style = style.Background(r.under)
		}
		r.b.WriteString(style.Render(r.cur.String()))
		r.cur.Reset()
	}
}

// soften is a colour halfway to the background, for the body of a graph
// under its bright edge. Only an RGB colour can be mixed; the background is
// the theme's, or black or white as the terminal is dark or light.
func soften(c lipgloss.TerminalColor) (lipgloss.TerminalColor, bool) {
	fgc, ok := c.(lipgloss.Color)
	if !ok {
		return c, false
	}
	bg := "#000000"
	if !darkTerminal {
		bg = "#ffffff"
	}
	if theme != nil && strings.HasPrefix(theme.PanelBG, "#") {
		bg = theme.PanelBG
	}
	a, okA := rgb(string(fgc))
	b, okB := rgb(bg)
	if !okA || !okB {
		return c, false
	}
	mix := func(x, y uint64) uint64 { return (x*55 + y*45) / 100 }
	return lipgloss.Color("#" + hex2(mix(a[0], b[0])) + hex2(mix(a[1], b[1])) + hex2(mix(a[2], b[2]))), true
}

func rgb(c string) ([3]uint64, bool) {
	if len(c) != 7 || c[0] != '#' {
		return [3]uint64{}, false
	}
	v, err := strconv.ParseUint(c[1:], 16, 32)
	if err != nil {
		return [3]uint64{}, false
	}
	return [3]uint64{v >> 16 & 0xff, v >> 8 & 0xff, v & 0xff}, true
}

func hex2(n uint64) string {
	s := strconv.FormatUint(n, 16)
	if len(s) == 1 {
		return "0" + s
	}
	return s
}

func (r *runs) String() string {
	r.flush()
	return r.b.String()
}

// spark is a one-line graph of vals, the newest on the right, width cells
// wide, top the value that fills a cell. Before there's history, the
// baseline.
func spark(vals []float64, width int, top float64, hue lipgloss.TerminalColor, levelOf func(float64) level) string {
	if width <= 0 {
		return ""
	}
	if len(vals) > width {
		vals = vals[len(vals)-width:]
	}
	var r runs
	r.add(look.Surface, strings.Repeat("▁", width-len(vals)))
	for _, v := range vals {
		if v <= 0 || top <= 0 {
			r.add(look.Surface, "▁")
			continue
		}
		i := min(7, max(0, int(math.Ceil(v/top*8))-1))
		r.add(levelColor(hue, levelOf(v)), string(eighths[i]))
	}
	return r.String()
}

// graph is spark several lines high: an area, the newest on the right. Its
// top edge is drawn bright and what's under it faint, so a busy machine is a
// line you can follow, not a wall. The edge is a cell thick wherever the
// line is: from the value down a whole cell's height, splitting the cell it
// ends in (soft below, bright above: the glyph's colour and its background),
// so the same value looks the same whichever row it falls in.
func graph(vals []float64, width, height int, top float64, hue lipgloss.TerminalColor, levelOf func(float64) level) []string {
	if height <= 1 {
		return []string{spark(vals, width, top, hue, levelOf)}
	}
	if len(vals) > width {
		vals = vals[len(vals)-width:]
	}
	lines := make([]string, height)
	for row := range height {
		var r runs
		from := float64(height-1-row) * 8 // eighths below this row
		r.add(look.Surface, strings.Repeat(baseline(row, height), width-len(vals)))
		for _, v := range vals {
			fill := 0.0
			if top > 0 {
				fill = v / top * float64(height) * 8
			}
			c := levelColor(hue, levelOf(v))
			cell := min(8, max(0, fill-from))
			// How much of this cell is below the edge: the body, soft.
			body := math.Round(min(8, max(0, fill-8-from)))
			switch {
			case cell < 0.5:
				r.add(look.Surface, baseline(row, height))
			case body < 2:
				// The edge's top, or all of it: bright, to the value. (A
				// sliver of body under it, an eighth, would only flicker:
				// the edge is a little thicker instead.)
				r.add(c, string(eighths[min(7, int(math.Round(cell))-1)]))
			case body >= 8:
				r.addFaint(c, true, "█") // under the edge: soft
			default:
				// Where the edge ends: soft up to it, bright above.
				r.addCell(c, true, c, string(eighths[int(body)-1]))
			}
		}
		lines[row] = r.String()
	}
	return lines
}

// baseline is what an empty cell of a graph shows: the floor on its bottom
// row, nothing above.
func baseline(row, height int) string {
	if row == height-1 {
		return "▁"
	}
	return " "
}

// gauge is a bar width cells long, filled to pct (0–100), to the half cell.
func gauge(pct float64, width int, c lipgloss.TerminalColor) string {
	if width <= 0 {
		return ""
	}
	halves := int(math.Round(max(0, min(100, pct)) / 100 * float64(width) * 2))
	if pct > 0 && halves == 0 {
		halves = 1 // something, however little, shows
	}
	full, half := halves/2, halves%2 == 1
	out := fg(c).Render(strings.Repeat("━", full))
	rest := width - full
	if half {
		out += fg(c).Render("╸")
		rest--
	}
	return out + fg(look.Surface).Render(strings.Repeat("━", rest))
}

// cells is a row of small bars, one a core: how evenly the work is spread.
func cells(vals []float64, hue lipgloss.TerminalColor) string {
	var r runs
	for _, v := range vals {
		if v < 1 {
			r.add(look.Surface, "▁")
			continue
		}
		r.add(levelColor(hue, pctLevel(v)), string(eighths[min(7, int(v/100*8))]))
	}
	return r.String()
}

// ── layout helpers ────────────────────────────────────────────────────────────

// fit cuts s to width cells, with an ellipsis if it had to.
func fit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= width {
		return s
	}
	return ansi.Truncate(s, width-1, "") + "…"
}

// pad fills s out to width cells.
func pad(s string, width int) string {
	if w := lipgloss.Width(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

// spread puts left at the start and right at the end of width cells, the
// left one cut short if both don't fit.
func spread(left, right string, width int) string {
	rw := lipgloss.Width(right)
	left = fit(left, max(0, width-rw-1))
	gap := width - lipgloss.Width(left) - rw
	if gap < 1 {
		return fit(left+" "+right, width)
	}
	return left + strings.Repeat(" ", gap) + right
}

// ── the parts of a card ───────────────────────────────────────────────────────

// labelWidth is the width of a row's label: "LOAD ".
const labelWidth = 5

// row is a card's line for one reading: its label, its graph or gauge, and
// its value: a main part, and a quieter one after (styled by the caller,
// dim but for what wants a look).
func row(label, mark, main, rest string, width int) string {
	valueW := 11
	if width < 34 {
		valueW = 6 // narrow: the main part only
		rest = ""
	}
	value := main
	if rest != "" {
		value += " " + rest
	}
	value = fit(value, valueW)
	return styleDim.Render(pad(label, labelWidth)) + mark + " " + lipgloss.PlaceHorizontal(valueW, lipgloss.Right, value)
}

// markWidth is how wide a row's graph or gauge is in a card width wide.
func markWidth(width int) int {
	if width < 34 {
		return max(1, width-labelWidth-1-6)
	}
	return max(1, width-labelWidth-1-11)
}

// cpuRow, loadRow… are a live machine's rows.
func cpuRow(h *hostState, w int) string {
	l := pctLevel(h.v.CPU)
	return row("CPU", spark(h.hist.CPU.vals, markWidth(w), 100, hueCPU(), pctLevel), valueStyle(l).Render(pctText(h.v.CPU)), "", w)
}

func loadRow(h *hostState, w int) string {
	l := loadLevel(h.v.LoadPerCore)
	// A graph of load a core, full at twice as much work as cores.
	return row("LOAD", spark(h.hist.Load.vals, markWidth(w), 2, hueLoad(), loadLevel),
		valueStyle(l).Render(strconv.FormatFloat(h.v.LoadPerCore, 'f', 2, 64)),
		styleDim.Render(strconv.FormatFloat(h.last.Load[0], 'f', 1, 64)), w)
}

func memRow(h *hostState, w int) string {
	l := pctLevel(h.v.Mem)
	return row("MEM", gauge(h.v.Mem, markWidth(w), levelColor(hueMem(), l)),
		valueStyle(l).Render(pctText(h.v.Mem)), styleDim.Render(bytesText(h.last.MemTotal)), w)
}

func swapRow(h *hostState, w int) string {
	l := levelOK
	if h.v.Swap >= 50 {
		l = levelWarn
	}
	return row("SWAP", gauge(h.v.Swap, markWidth(w), levelColor(hueMem(), l)),
		valueStyle(l).Render(pctText(h.v.Swap)), styleDim.Render(bytesText(h.last.SwapTotal)), w)
}

// diskRow is the filesystem that most wants you: the one filling soonest,
// if one will be full within the week, or else the fullest.
func diskRow(h *hostState, w int) string {
	d := urgentDisk(h)
	if d == nil {
		return row("DISK", fg(look.Surface).Render(strings.Repeat("━", markWidth(w))), styleDim.Render("–"), "", w)
	}
	p := diskPct(*d)
	rest := styleDim.Render(shortMount(d.Mount))
	if left, ok := h.fills[d.Mount]; ok && fillLevel(left) > levelOK {
		// How soon, in place of where: the filesystem's in the title's pill.
		rest = valueStyle(fillLevel(left)).Render("full " + strings.TrimPrefix(fillText(left), "~"))
	}
	return row("DISK", gauge(p, markWidth(w), levelColor(hueDisk(), diskLevel(h, *d))), valueStyle(pctLevel(p)).Render(pctText(p)), rest, w)
}

// diskLevel is how much a filesystem wants you: for how full it is, or how
// soon it will be.
func diskLevel(h *hostState, d disk) level {
	l := pctLevel(diskPct(d))
	if left, ok := h.fills[d.Mount]; ok {
		l = max(l, fillLevel(left))
	}
	return l
}

func urgentDisk(h *hostState) *disk {
	var soonest *disk
	for i := range h.last.Disks {
		d := &h.last.Disks[i]
		left, ok := h.fills[d.Mount]
		if ok && fillLevel(left) > levelOK && (soonest == nil || left < h.fills[soonest.Mount]) {
			soonest = d
		}
	}
	if soonest != nil {
		return soonest
	}
	return fullestDisk(h.last)
}

func netRow(h *hostState, w int) string {
	top := max(h.hist.Net.max(historyLen), 64*1024) // against the last few minutes; a quiet line stays low
	return row("NET", spark(h.hist.Net.vals, markWidth(w), top, hueNet(), calm),
		styleLead.Render("↓"+bytesText(uint64(h.v.NetRx))), styleDim.Render("↑"+bytesText(uint64(h.v.NetTx))), w)
}

func ioRow(h *hostState, w int) string {
	top := max(h.hist.Disk.max(historyLen), 1<<20)
	return row("IO", spark(h.hist.Disk.vals, markWidth(w), top, hueDisk(), calm),
		styleLead.Render("r"+bytesText(uint64(h.v.DiskRead))), styleDim.Render("w"+bytesText(uint64(h.v.DiskWrite))), w)
}

func gpuRow(h *hostState, w int) string {
	g := h.last.GPUs[0]
	return row("GPU", spark(h.gpu.vals, markWidth(w), 100, hueGPU(), pctLevel),
		valueStyle(max(pctLevel(g.Util), tempLevel(g.Temp))).Render(pctText(g.Util)), styleDim.Render(itoa(int(g.Temp))+"°C"), w)
}

// calm is a level for readings that have no line to cross: traffic.
func calm(float64) level { return levelOK }

// shortMount is a mount point that fits a card: "/", "/data", "…/media".
func shortMount(m string) string {
	if m == "/System/Volumes/Data" {
		return "/"
	}
	if len(m) > 6 {
		if i := strings.LastIndexByte(m, '/'); i > 0 {
			return "…" + fit(m[i:], 5)
		}
		return fit(m, 6)
	}
	return m
}

// subtitle is what a machine is, briefly: "Ubuntu 24.04 · 16 cores · up 41d".
func subtitle(h *hostState) string {
	s := h.last
	parts := []string{releaseText(s)}
	if s.Cores > 0 {
		parts = append(parts, itoa(s.Cores)+" cores")
	}
	if u := uptimeText(s.Uptime); u != "" {
		parts = append(parts, "up "+u)
	}
	return strings.Join(parts, " · ")
}

// counts is the line of what's running: "418 procs · 2.1k threads · 54°".
func counts(h *hostState) string {
	s := h.last
	var parts []string
	if s.Processes > 0 {
		parts = append(parts, countText(s.Processes)+" procs")
	}
	if s.Threads > 0 {
		parts = append(parts, countText(s.Threads)+" threads")
	}
	if s.TempC > 0 {
		t := itoa(int(math.Round(s.TempC))) + "°C"
		if l := tempLevel(s.TempC); l != levelOK {
			parts = append(parts, valueStyle(l).Render(t))
		} else {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, styleDim.Render(" · "))
}

// topLine is the busiest process: what's eating the machine, when
// something is.
func topLine(h *hostState, w int) string {
	if len(h.v.Top) == 0 {
		return ""
	}
	p := h.v.Top[0]
	cpu := valueStyle(procLevel(p.CPU, h.last.Cores)).Render(pctText(p.CPU))
	right := cpu + " " + styleDim.Render(bytesText(p.RSS))
	return spread(styleDim.Render("▸ ")+styleText.Render(p.Name), right, w)
}

// procLevel is how hard a process works its machine: a whole machine's
// worth is hot, most of one core is only busy.
func procLevel(cpu float64, cores int) level {
	if cores > 0 && cpu >= float64(cores)*100*0.9 {
		return levelHot
	}
	if cpu >= 90 && (cores <= 1 || cpu >= float64(cores)*100*0.5) {
		return levelWarn
	}
	return levelOK
}

// statusMark is what's at the right of a card's title: a quiet dot while
// all's well, a pill saying what isn't.
func (m model) statusMark(h *hostState) string {
	switch {
	case h.status == "auth":
		return statePill(styleErr, "SSH AUTH")
	case h.status == "down":
		return statePill(styleDim, "OFFLINE")
	case !h.up():
		return styleDim.Render("connecting…")
	case h.level == levelHot:
		return statePill(styleErr, "HOT "+h.why)
	case h.level == levelWarn:
		return statePill(styleWarn, "WARN "+h.why)
	}
	return styleOK.Render("●")
}

// ── cards ─────────────────────────────────────────────────────────────────────

// cardLines is a machine's card inside its border, w cells wide.
func (m model) cardLines(h *hostState, w int) []string {
	name := styleTitle.Render(h.t.Name)
	if h.t.Origin == "this" {
		name += styleDim.Render(" · this machine")
	}
	lines := []string{spread(name, m.statusMark(h), w)}
	if h.last == nil || (!h.up() && h.status != "connecting") {
		return append(lines, m.downLines(h, w)...)
	}
	lines = append(lines, styleDim.Render(fit(subtitle(h), w)), "",
		cpuRow(h, w), loadRow(h, w), memRow(h, w))
	if h.last.SwapTotal > 0 && h.v.Swap >= 10 {
		lines = append(lines, swapRow(h, w))
	}
	if len(h.last.GPUs) > 0 {
		lines = append(lines, gpuRow(h, w))
	}
	lines = append(lines, diskRow(h, w))
	if h.last.HasDiskIO {
		lines = append(lines, ioRow(h, w))
	}
	lines = append(lines, netRow(h, w))
	lines = append(lines, "", fit(counts(h), w))
	if t := topLine(h, w); t != "" {
		lines = append(lines, t)
	}
	return lines
}

// downLines is what a card says of a machine that isn't answering: why,
// and what to do about it.
func (m model) downLines(h *hostState, w int) []string {
	if h.status == "connecting" {
		return []string{styleDim.Render(fit("Connecting to "+h.t.SSH+"…", w))}
	}
	var lines []string
	if h.last != nil {
		lines = append(lines, styleDim.Render(fit(subtitle(h), w)))
	} else {
		lines = append(lines, styleDim.Render(fit(h.t.SSH, w)))
	}
	lines = append(lines, "")
	for _, l := range wrap(h.err, w) {
		lines = append(lines, styleText.Render(l))
	}
	lines = append(lines, "")
	switch {
	case h.status == "auth" && h.t.Origin == "herdr":
		lines = append(lines, styleDim.Render(fit("Log in again: herdr machine reconnect "+h.t.Name, w)))
	case h.status == "auth":
		lines = append(lines, styleDim.Render(fit("ssh needs you: try ssh "+h.t.SSH+" in a pane", w)))
	}
	if !h.retry.IsZero() {
		secs := int(math.Ceil(h.retry.Sub(m.now).Seconds()))
		if secs > 0 {
			lines = append(lines, styleDim.Render("Trying again in "+itoa(secs)+"s · r now"))
		} else {
			lines = append(lines, styleDim.Render("Trying again…"))
		}
	}
	return lines
}

// wrap breaks text into lines of at most width cells, at spaces.
func wrap(s string, width int) []string {
	if width <= 0 {
		return nil
	}
	var lines []string
	line := ""
	for _, word := range strings.Fields(s) {
		switch {
		case line == "":
			line = word
		case ansi.StringWidth(line)+1+ansi.StringWidth(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	for i, l := range lines {
		lines[i] = fit(l, width)
	}
	return lines
}

// layout is how the cards sit: cols to a row, each w wide (border
// included), the grid starting x0 cells in.
type layout struct {
	cols, w, x0 int
}

const (
	minCard = 40 // a card narrower than this crowds its graphs
	maxCard = 64 // wider and its graphs are more history than glance
	cardGap = 1
)

func (m model) grid() layout {
	n := max(1, len(m.hosts))
	avail := m.width - 2 // a cell of margin either side
	cols := max(1, min(n, (avail+cardGap)/(minCard+cardGap)))
	w := min(maxCard, (avail-(cols-1)*cardGap)/cols)
	w = max(min(avail, 24), w)
	used := cols*w + (cols-1)*cardGap
	return layout{cols: cols, w: w, x0: max(1, (m.width-used)/2)}
}

// card is a machine's card, border and all, as tall as height.
func (m model) card(i int, w, height int) []string {
	h := m.hosts[i]
	inner := m.cardLines(h, w-4)
	for len(inner) < height-2 {
		inner = append(inner, "")
	}
	border := look.Surface
	switch {
	case i == m.cursor:
		border = look.Accent
	case h.status == "auth":
		border = look.Red
	case h.up() && h.level == levelHot:
		border = look.Red
	case h.up() && h.level == levelWarn:
		border = look.Yellow
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).
		Padding(0, 1).Width(w - 2)
	return strings.Split(box.Render(strings.Join(inner, "\n")), "\n")
}

// rows is the machines in the order shown, a row of the grid at a time.
func (m model) rows() [][]int {
	cols := m.grid().cols
	var out [][]int
	o := m.order()
	for i := 0; i < len(o); i += cols {
		out = append(out, o[i:min(len(o), i+cols)])
	}
	return out
}

// rowHeight is how tall a row of cards is: its tallest card's.
func (m model) rowHeight(r []int) int {
	w := m.grid().w
	hgt := 0
	for _, i := range r {
		hgt = max(hgt, len(m.cardLines(m.hosts[i], w-4))+2)
	}
	return hgt
}

// viewGrid is the cards, from the scrolled-to row, in height lines: whole
// cards only, and a line saying how many more there are below.
func (m model) viewGrid(height int) []string {
	l := m.grid()
	var out []string
	rows := m.rows()
	for ri, r := range rows {
		if ri < m.scroll {
			continue
		}
		hgt := m.rowHeight(r)
		if len(out) > 0 && len(out)+hgt > height-1 {
			more := 0
			for _, rest := range rows[ri:] {
				more += len(rest)
			}
			out = append(out, strings.Repeat(" ", l.x0)+styleDim.Render("↓ "+itoa(more)+" more"))
			break
		}
		cards := make([][]string, len(r))
		for c, i := range r {
			cards[c] = m.card(i, l.w, hgt)
		}
		for y := range hgt {
			line := strings.Repeat(" ", l.x0)
			for c := range cards {
				if c > 0 {
					line += strings.Repeat(" ", cardGap)
				}
				line += pad(cards[c][y], l.w)
			}
			out = append(out, line)
		}
	}
	return out
}

// cardAt is the machine whose card is at x, y on the screen.
func (m model) cardAt(x, y int) (int, bool) {
	l := m.grid()
	top := 2 // the header and the blank under it
	for ri, r := range m.rows() {
		if ri < m.scroll {
			continue
		}
		hgt := m.rowHeight(r)
		if y >= top && y < top+hgt {
			for c, i := range r {
				x0 := l.x0 + c*(l.w+cardGap)
				if x >= x0 && x < x0+l.w {
					return i, true
				}
			}
			return 0, false
		}
		top += hgt
	}
	return 0, false
}

// keepInView scrolls the grid so the selected machine's row is on screen.
func (m *model) keepInView() {
	if m.height == 0 || len(m.hosts) == 0 {
		return
	}
	rows := m.rows()
	want := m.position() / m.grid().cols
	if want < m.scroll {
		m.scroll = want
	}
	for m.scroll < want {
		used := 0
		for _, r := range rows[m.scroll : want+1] {
			used += m.rowHeight(r)
		}
		if used <= m.height-3 {
			break
		}
		m.scroll++
	}
	m.scroll = max(0, min(m.scroll, len(rows)-1))
}

// ── the header ────────────────────────────────────────────────────────────────

// viewHeader is how many machines want you, and that it's live. The
// popup's border has the plugin's name.
func (m model) viewHeader() string {
	var hot, warn, ok, off int
	for _, h := range m.hosts {
		switch {
		case h.status == "auth" || h.status == "down":
			off++
		case !h.up():
		case h.level == levelHot:
			hot++
		case h.level == levelWarn:
			warn++
		default:
			ok++
		}
	}
	left := " "
	if m.detail && !m.single() {
		left += styleDim.Render(strconv.Itoa(m.position()+1) + " of " + strconv.Itoa(len(m.hosts)) + " machines")
	} else {
		var parts []string
		if hot > 0 {
			parts = append(parts, statePill(styleErr, itoa(hot)+" hot"))
		}
		if warn > 0 {
			parts = append(parts, statePill(styleWarn, itoa(warn)+" warn"))
		}
		if ok > 0 {
			parts = append(parts, styleOK.Render(itoa(ok)+" calm"))
		}
		if off > 0 {
			parts = append(parts, styleDim.Render(itoa(off)+" offline"))
		}
		if len(parts) == 0 {
			parts = append(parts, styleDim.Render("connecting…"))
		}
		left += strings.Join(parts, "  ")
	}
	dot := styleOK.Render("●")
	if m.beat {
		dot = fg(look.Green).Faint(true).Render("●")
	}
	right := dot + styleDim.Render(" live · every "+durationText(m.cfg.interval())) + " "
	if m.demo {
		right = styleDim.Render("demo · ") + right
	}
	// Narrow, the counts come first: the dot alone says it's live.
	if lipgloss.Width(left)+lipgloss.Width(right)+2 > m.width {
		right = dot + " "
	}
	return spread(left, right, m.width)
}

// durationText is an interval, briefly: "2s", "1.5s".
func durationText(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s"
}

// ── the strip ─────────────────────────────────────────────────────────────────

// viewStrip is every machine on a line: its name, a small graph of its load
// a core, and the load. For a pane only a line or two high.
func (m model) viewStrip() string {
	var full, short []string
	for _, i := range m.order() {
		h := m.hosts[i]
		name := styleTitle.Render(h.t.Name)
		if !h.up() {
			mark := styleDim.Render("…")
			if h.status == "auth" || h.status == "down" {
				mark = styleErr.Render("offline")
			}
			full, short = append(full, name+" "+mark), append(short, name+" "+mark)
			continue
		}
		l := loadLevel(h.v.LoadPerCore)
		cell := name + " " + spark(h.hist.Load.vals, 8, 2, hueLoad(), loadLevel) + " " +
			valueStyle(l).Render(strconv.FormatFloat(h.v.LoadPerCore, 'f', 2, 64))
		short = append(short, cell)
		cell += styleDim.Render(" cpu ") + valueStyle(pctLevel(h.v.CPU)).Render(pctText(h.v.CPU)) +
			styleDim.Render(" mem ") + valueStyle(pctLevel(h.v.Mem)).Render(pctText(h.v.Mem))
		full = append(full, cell)
	}
	sep := styleDim.Render("  │  ")
	line := " " + strings.Join(full, sep)
	if lipgloss.Width(line) > m.width {
		line = " " + strings.Join(short, sep) // the graph and the load, no more
	}
	return fit(line, m.width)
}
