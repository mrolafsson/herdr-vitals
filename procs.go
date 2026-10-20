package main

import (
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The processes of one machine, the whole screen of them: what's using its
// CPU, or its memory, now.

// procsByMem is the processes by how much memory they hold, the most first.
func procsByMem(ps []procRate) []procRate {
	out := append([]procRate(nil), ps...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].RSS > out[j].RSS })
	return out
}

// shownProcs is the processes in the order the screen has them.
func (m model) shownProcs(h *hostState) []procRate {
	if m.byMem {
		return procsByMem(h.v.Top)
	}
	return h.v.Top
}

// procHeader is the line over the processes, its columns named.
func procHeader(byMem bool, barW int) string {
	cpu, mem := "CPU", "MEM"
	if byMem {
		mem = styleTabOn.Render(mem)
	} else {
		cpu = styleTabOn.Render(cpu)
	}
	cols := lipgloss.PlaceHorizontal(barW+7, lipgloss.Right, cpu) + lipgloss.PlaceHorizontal(7, lipgloss.Right, mem) +
		lipgloss.PlaceHorizontal(5, lipgloss.Right, "THR") + lipgloss.PlaceHorizontal(9, lipgloss.Right, "PID") + "  PROCESS"
	return styleDim.Render(cols)
}

// procLine is a process: a bar of what it's sorted by, as a share of the
// most any process on the list has (top), so they can be told apart; its
// CPU (a core is 100%), memory, threads, pid and name.
func procLine(p procRate, h *hostState, byMem bool, top float64, barW, width int) string {
	l := procLevel(p.CPU, h.last.Cores)
	bar := ""
	if barW > 0 && top > 0 {
		if byMem {
			bar = gauge(100*float64(p.RSS)/top, barW, hueMem())
		} else {
			bar = gauge(100*p.CPU/top, barW, levelColor(hueCPU(), l))
		}
	}
	thr := ""
	if p.Threads > 0 {
		thr = itoa(p.Threads)
	}
	line := bar + lipgloss.PlaceHorizontal(7, lipgloss.Right, valueStyle(l).Render(pctText(p.CPU))) +
		lipgloss.PlaceHorizontal(7, lipgloss.Right, styleText.Render(bytesText(p.RSS))) +
		lipgloss.PlaceHorizontal(5, lipgloss.Right, styleDim.Render(thr)) +
		lipgloss.PlaceHorizontal(9, lipgloss.Right, styleDim.Render(itoa(p.PID))) + "  " + styleLead.Render(p.Name)
	return fit(line, width)
}

// procBarWidth is how wide the bars are on a screen width wide: room for
// them, and the names after.
func procBarWidth(width int) int {
	return max(0, min(24, width-7-7-5-9-2-24))
}

// viewProcs is the machine's processes, scrolled to m.procScroll, in height
// lines.
func (m model) viewProcs(h *hostState, height int) []string {
	w := m.width - 4
	lines := m.detailHead(h, w)
	if !h.up() {
		return indent(append(append(lines, ""), m.downLines(h, w)...))
	}
	s := h.last
	sum := []string{countText(s.Processes) + " processes"}
	if s.Threads > 0 {
		sum = append(sum, countText(s.Threads)+" threads")
	}
	sum = append(sum, "CPU "+pctText(h.v.CPU)+" of "+itoa(s.Cores)+" cores",
		"memory "+bytesText(s.MemTotal-min(s.MemAvail, s.MemTotal))+" of "+bytesText(s.MemTotal))
	barW := procBarWidth(w)
	lines = append(lines, styleDim.Render(fit(strings.Join(sum, " · "), w)), "", fit(procHeader(m.byMem, barW), w))
	ps := m.shownProcs(h)
	top := 100.0 // a core, at least: an idle machine's bars stay short
	for _, p := range ps {
		if m.byMem {
			top = max(top, float64(p.RSS))
		} else {
			top = max(top, p.CPU)
		}
	}
	room := max(1, height-len(lines))
	from := max(0, min(m.procScroll, len(ps)-room))
	for _, p := range ps[from:min(len(ps), from+room)] {
		lines = append(lines, procLine(p, h, m.byMem, top, barW, w))
	}
	if len(ps) == 0 {
		lines = append(lines, styleDim.Render("Reading… a process needs two readings for its CPU."))
	}
	return indent(lines)
}
