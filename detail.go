package main

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The details of one machine: every reading as a graph of its last few
// minutes, each core, every filesystem and the busiest processes.

// viewDetail is the machine's details for height lines: the graphs as tall
// as leave room for the lists under them. When even the smallest don't,
// it's all of it, and the screen scrolls.
func (m model) viewDetail(h *hostState, height int) []string {
	w := m.width - 4 // two cells of margin either side
	head := m.detailHead(h, w)
	if !h.up() {
		return indent(append(head, append([]string{""}, m.downLines(h, w)...)...))
	}
	// The only machine: the AI accounts here too, as there's no card for
	// them: every limit when there's room, else each account's week, else
	// none (the header names the nearest, and a has them all). The graphs
	// keep their size first; then the most of the accounts that fits.
	variants := [][]string{nil}
	if m.single() {
		variants = nil
		for _, v := range [][]string{m.aiCompact(w), m.aiWeeks(w)} {
			if len(v) > 0 {
				variants = append(variants, append([]string{""}, v...))
			}
		}
		variants = append(variants, nil)
	}
	sizes := [][2]int{{6, 4}, {5, 3}, {4, 3}, {3, 2}, {2, 2}, {2, 1}, {1, 1}}
	if m.mode == modePane {
		sizes = sizes[3:] // beside your work, a glance: small graphs
	}
	lists := m.detailLists(h, w)
	for _, size := range sizes {
		graphs := m.detailGraphs(h, w, size[0], size[1])
		for _, ai := range variants {
			top := append(append([]string(nil), graphs...), ai...)
			if len(head)+len(top)+len(lists)+2 <= height || (size[0] == 1 && ai == nil) {
				lines := append(head, "")
				lines = append(lines, top...)
				lines = append(lines, "")
				lines = append(lines, lists...)
				return indent(lines)
			}
		}
	}
	return nil
}

func indent(lines []string) []string {
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return lines
}

// detailHead is the machine's name, how it is, and what it is.
func (m model) detailHead(h *hostState, w int) []string {
	title := styleTitle.Render(h.t.Name) + "  " + m.statusMark(h)
	if h.t.SSH != "" && h.t.SSH != h.t.Name {
		title += styleDim.Render("  " + h.t.SSH)
	}
	if h.last == nil {
		return []string{fit(title, w)}
	}
	s := h.last
	about := []string{releaseText(s), s.Kernel, s.Arch}
	if s.Cores > 0 {
		about = append(about, itoa(s.Cores)+" cores")
	}
	if u := uptimeText(s.Uptime); u != "" {
		about = append(about, "up "+u)
	}
	line := styleDim.Render(strings.Join(nonEmpty(about), " · "))
	// Both on a line when they fit; else the name keeps its line, and what
	// the machine is goes under it.
	if lipgloss.Width(title)+2+lipgloss.Width(line) <= w {
		return []string{spread(title, line, w)}
	}
	return []string{fit(title, w), fit(line, w)}
}

func nonEmpty(ss []string) []string {
	var out []string
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// heading is a section's first line: its name, its reading, then more about
// it, dim.
func heading(name, value, more string) string {
	out := styleHeader.Render(name) + "  " + value
	if more != "" {
		out += styleDim.Render("  " + more)
	}
	return out
}

// chart is a graph with its heading: the name, the reading in the graph's
// colour (its level's, when it wants a look), more about it dim; then a
// rule in that colour along the graph's top, saying what reaches it.
func chart(name, value, more string, hue lipgloss.TerminalColor, l level, width int,
	vals []float64, height int, top float64, topLabel string, levelOf func(float64) level) []string {
	head := heading(name, readingStyle(hue, l).Render(value), more)
	return append([]string{fit(head, width), ceiling(width, hue, topLabel)},
		graph(vals, width, height, top, hue, levelOf)...)
}

// ceiling is the line over a graph, in its colour but quieter, with the
// value that fills the graph at its end: "100%", "6.2M/s".
func ceiling(width int, hue lipgloss.TerminalColor, label string) string {
	c, ok := soften(hue)
	style := fg(c)
	if !ok {
		style = style.Faint(true)
	}
	label = " " + label
	return style.Render(strings.Repeat("─", max(0, width-lipgloss.Width(label)))) + styleDim.Render(label)
}

func loadText(l float64) string { return strconv.FormatFloat(l, 'f', 2, 64) }

// series is what a graph width wide draws: the last few minutes from the
// machine's own history (live), or with the day on, its last day from the
// sampler's minutes, each column the most its minutes reached (pick).
func (m model) series(live []float64, width int, pick func(point) float64) []float64 {
	if !m.day {
		return live
	}
	return dayColumns(m.dayOf[m.cursor], width, m.now, pick)
}

// span is what a graph's ceiling says it spans, with the day on.
func (m model) span(label string) string {
	if m.day {
		return label + " · 24h"
	}
	return label
}

// seriesMax is the most of a graph's values: its top, for traffic.
func seriesMax(vals []float64) float64 {
	top := 0.0
	for _, v := range vals {
		top = max(top, v)
	}
	return top
}

// timeAxis is the line under the day's CPU graph: when its parts were.
func timeAxis(width int) string {
	marks := []string{"24h ago", "18h", "12h", "6h", "now"}
	line := []rune(strings.Repeat(" ", width))
	for i, mk := range marks {
		x := i * (width - 1) / (len(marks) - 1)
		x = max(0, min(width-len(mk), x-len(mk)/2))
		if i == 0 {
			x = 0
		}
		copy(line[x:], []rune(mk))
	}
	return styleDim.Render(string(line))
}

// detailGraphs is the CPU across the width, gh lines high, its cores under
// it; then load and memory beside network and disk traffic, gh2 high.
func (m model) detailGraphs(h *hostState, w, gh, gh2 int) []string {
	s, v := h.last, h.v
	cpuMore := ""
	if s.OS != "darwin" {
		cpuMore = "user " + pctText(v.User) + " · system " + pctText(v.System) + " · iowait " + pctText(v.IOWait)
		if v.Steal >= 0.5 {
			cpuMore += " · steal " + pctText(v.Steal)
		}
	}
	out := chart("CPU", pctText(v.CPU), cpuMore, hueCPU(), pctLevel(v.CPU), w,
		m.series(h.hist.CPU.vals, w, func(p point) float64 { return max(p.CPU, p.CPUMax) }), gh, 100, m.span("100%"), pctLevel)
	if m.day {
		out = append(out, timeAxis(w))
	} else if len(v.Cores) > 1 {
		label := styleDim.Render("cores ")
		per := max(1, w-6)
		for i := 0; i < len(v.Cores); i += per {
			out = append(out, label+cells(v.Cores[i:min(len(v.Cores), i+per)], hueCPU()))
			label = "      "
		}
	}
	out = append(out, "")

	// Two columns when there's room for graphs worth reading, else one.
	colW := w
	if w >= 80 {
		colW = (w - 3) / 2
	}
	load := chart("LOAD", loadText(v.LoadPerCore)+" a core", loadText(s.Load[0])+" "+loadText(s.Load[1])+" "+loadText(s.Load[2]),
		hueLoad(), loadLevel(v.LoadPerCore), colW, m.series(h.hist.Load.vals, colW, func(p point) float64 { return max(p.Load, p.LoadMax) }),
		gh2, 2, m.span("2.00"), loadLevel)
	memMore := bytesText(s.MemTotal-min(s.MemAvail, s.MemTotal)) + " of " + bytesText(s.MemTotal)
	if s.SwapTotal > 0 {
		memMore += " · swap " + bytesText(s.SwapUsed) + " of " + bytesText(s.SwapTotal)
	}
	mem := chart("MEMORY", pctText(v.Mem), memMore, hueMem(), pctLevel(v.Mem), colW,
		m.series(h.hist.Mem.vals, colW, func(p point) float64 { return p.Mem }), gh2, 100, m.span("100%"), pctLevel)
	// Traffic has no ceiling of its own: the graph's top is the most of the
	// last few minutes, and the rule says what that was.
	netVals := m.series(h.hist.Net.vals, colW, func(p point) float64 { return p.Net })
	netTop := max(seriesMax(netVals), 64*1024)
	net := chart("NETWORK", "↓ "+bytesText(uint64(v.NetRx))+"/s", "↑ "+bytesText(uint64(v.NetTx))+"/s", hueNet(), levelOK, colW,
		netVals, gh2, netTop, m.span(bytesText(uint64(netTop))+"/s"), calm)
	left, right := append(load, append([]string{""}, mem...)...), net
	if s.HasDiskIO {
		ioVals := m.series(h.hist.Disk.vals, colW, func(p point) float64 { return p.Disk })
		ioTop := max(seriesMax(ioVals), 1<<20)
		io := chart("DISK IO", "read "+bytesText(uint64(v.DiskRead))+"/s", "write "+bytesText(uint64(v.DiskWrite))+"/s", hueDisk(), levelOK, colW,
			ioVals, gh2, ioTop, m.span(bytesText(uint64(ioTop))+"/s"), calm)
		right = append(right, append([]string{""}, io...)...)
	}
	for i, g := range s.GPUs {
		more := strconv.FormatFloat(g.MemUsed/1024, 'f', 1, 64) + "G of " + strconv.FormatFloat(g.MemTotal/1024, 'f', 0, 64) + "G · " + itoa(int(g.Temp)) + "°C"
		name := "GPU"
		if len(s.GPUs) > 1 {
			name += " " + itoa(i)
		}
		l := max(pctLevel(g.Util), tempLevel(g.Temp))
		if i == 0 && !m.day { // the sampler keeps no GPU history: no day of it
			right = append(right, "")
			right = append(right, chart(name, pctText(g.Util), more, hueGPU(), l, colW, h.gpu.vals, gh2, 100, "100%", pctLevel)...)
		} else {
			if i == 0 {
				right = append(right, "")
			}
			right = append(right, fit(heading(name, readingStyle(hueGPU(), l).Render(pctText(g.Util)), more), colW))
		}
	}
	return append(out, columns(left, right, colW, w)...)
}

// columns sets two blocks of lines side by side, colW each, or one under
// the other when the width only takes one.
func columns(left, right []string, colW, w int) []string {
	if colW == w {
		return append(append(left, ""), right...)
	}
	var out []string
	for i := range max(len(left), len(right)) {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out = append(out, pad(l, colW)+"   "+r)
	}
	return out
}

// detailLists is the filesystems beside the busiest processes, in height
// lines at most.
func (m model) detailLists(h *hostState, w int) []string {
	colW := w
	if w >= 80 {
		colW = (w - 3) / 2
	}
	// Stacked, the filesystems take what they need and the processes the rest.
	fs := []string{styleHeader.Render("FILESYSTEMS")}
	mountW := 0
	for _, d := range h.last.Disks {
		mountW = max(mountW, min(18, lipgloss.Width(d.Mount)))
	}
	for _, d := range h.last.Disks {
		p := diskPct(d)
		l := pctLevel(p)
		// What's free, and when it won't be if it's filling.
		free := styleDim.Render(bytesText(d.Avail) + " free")
		if left, ok := h.fills[d.Mount]; ok {
			free += " " + valueStyle(fillLevel(left)).Render("full "+fillText(left))
		}
		gw := max(4, min(30, colW-mountW-1-5-1-lipgloss.Width(free)-1))
		fs = append(fs, fit(pad(fit(d.Mount, mountW), mountW)+" "+gauge(p, gw, levelColor(hueDisk(), diskLevel(h, d)))+" "+
			lipgloss.PlaceHorizontal(4, lipgloss.Right, valueStyle(l).Render(pctText(p)))+" "+free, colW))
	}
	if len(h.last.Disks) == 0 {
		fs = append(fs, styleDim.Render("None worth showing."))
	}
	procs := []string{fit(styleHeader.Render("BUSIEST")+styleDim.Render("  t for every process"), colW)}
	for i, p := range h.v.Top {
		if i >= 5 {
			break
		}
		procs = append(procs, procLine(p, h, false, 0, 0, colW))
	}
	return columns(fs, procs, colW, w)
}
