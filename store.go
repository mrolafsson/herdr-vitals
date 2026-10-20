package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// What the sampler keeps of each machine, so that whatever opens next (the
// popup, the pane, herdr-recap's header) opens on graphs already full: the
// last few minutes, a point a round, and the last day, a point a minute.
// Each is a file a machine under the plugin's state folder, written whole
// and swapped in, so a reader never sees half of one.

// point is a machine at a moment, or over a minute.
type point struct {
	T       int64   `json:"t"` // unix seconds
	CPU     float64 `json:"c"` // %
	CPUMax  float64 `json:"cx,omitempty"`
	Load    float64 `json:"l"` // a core
	LoadMax float64 `json:"lx,omitempty"`
	Mem     float64 `json:"m"` // %
	Net     float64 `json:"n"` // bytes a second, in and out
	Disk    float64 `json:"d"` // bytes a second, read and written
}

func pointOf(at time.Time, v vitals) point {
	return point{T: at.Unix(), CPU: v.CPU, Load: v.LoadPerCore, Mem: v.Mem, Net: v.NetRx + v.NetTx, Disk: v.DiskRead + v.DiskWrite}
}

const (
	recentKept = historyLen     // the last few minutes, a point a round
	dayKept    = 24 * 60        // the last day, a point a minute
	dayWindow  = 24 * time.Hour // what the day's graphs span
)

// hostKey is the name a machine's files go by: "local", or its ssh target
// made safe for a file name.
func hostKey(t target) string {
	key := "local"
	if !t.local() {
		key = t.SSH
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' {
			return r
		}
		return '_'
	}, key)
}

func storePath(t target, kind string) string {
	return filepath.Join(stateDir(), "hosts", hostKey(t)+"."+kind+".json")
}

// readPoints reads a machine's recent or day points; none if there are none.
func readPoints(t target, kind string) []point {
	data, err := os.ReadFile(storePath(t, kind))
	if err != nil {
		return nil
	}
	var ps []point
	if json.Unmarshal(data, &ps) != nil {
		return nil
	}
	return ps
}

// writePoints writes them whole, then swaps the file in.
func writePoints(t target, kind string, ps []point) error {
	path := storePath(t, kind)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(ps)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// freshRecent is a machine's recent points, if the sampler wrote them a
// moment ago: older, there's a gap they'd hide, and the graph starts empty.
func freshRecent(t target, interval time.Duration, now time.Time) []point {
	ps := readPoints(t, "recent")
	if len(ps) == 0 || now.Sub(time.Unix(ps[len(ps)-1].T, 0)) > 3*interval+10*time.Second {
		return nil
	}
	return ps
}

// seed fills the graphs from points, oldest first.
func (h *history) seed(ps []point) {
	for _, p := range ps {
		h.CPU.push(p.CPU)
		h.Load.push(p.Load)
		h.Mem.push(p.Mem)
		h.Net.push(p.Net)
		h.Disk.push(p.Disk)
	}
}

// minute adds up a minute's points into one.
type minute struct {
	start int64 // its first second
	n     float64
	sum   point
}

// add takes a point in, returning the minute before it if this one starts
// a new minute.
func (m *minute) add(p point) (point, bool) {
	start := p.T - p.T%60
	var done point
	ended := m.n > 0 && start != m.start
	if ended {
		done = m.mean()
		*m = minute{}
	}
	if m.n == 0 {
		m.start = start
	}
	m.n++
	m.sum.CPU += p.CPU
	m.sum.Load += p.Load
	m.sum.Mem += p.Mem
	m.sum.Net += p.Net
	m.sum.Disk += p.Disk
	m.sum.CPUMax = max(m.sum.CPUMax, p.CPU)
	m.sum.LoadMax = max(m.sum.LoadMax, p.Load)
	return done, ended
}

func (m *minute) mean() point {
	return point{T: m.start, CPU: m.sum.CPU / m.n, CPUMax: m.sum.CPUMax, Load: m.sum.Load / m.n, LoadMax: m.sum.LoadMax,
		Mem: m.sum.Mem / m.n, Net: m.sum.Net / m.n, Disk: m.sum.Disk / m.n}
}

// dayColumns spreads a day of minutes over width columns, the oldest on the
// left and now on the right: each column the busiest its minutes got (pick),
// or -1 where there's nothing (the sampler wasn't running).
func dayColumns(ps []point, width int, now time.Time, pick func(point) float64) []float64 {
	cols := make([]float64, width)
	for i := range cols {
		cols[i] = -1
	}
	if width <= 0 {
		return cols
	}
	from := now.Add(-dayWindow).Unix()
	span := float64(dayWindow/time.Second) / float64(width)
	for _, p := range ps {
		if p.T < from {
			continue
		}
		c := min(width-1, int(float64(p.T-from)/span))
		cols[c] = max(cols[c], pick(p))
	}
	return cols
}
