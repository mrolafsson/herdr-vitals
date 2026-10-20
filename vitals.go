package main

import (
	"sort"
	"time"
)

// historyLen is how many rounds a graph remembers: four minutes at the
// default two seconds, more than the widest card shows.
const historyLen = 240

// ring is a metric's recent values, oldest first.
type ring struct{ vals []float64 }

func (r *ring) push(v float64) {
	r.vals = append(r.vals, v)
	if len(r.vals) > historyLen {
		r.vals = r.vals[len(r.vals)-historyLen:]
	}
}

// last is the newest n values (fewer if there aren't n yet).
func (r *ring) last(n int) []float64 {
	if n >= len(r.vals) {
		return r.vals
	}
	return r.vals[len(r.vals)-n:]
}

// avg is the mean of the newest n values: a spike that comes and goes in one
// round isn't a hot machine.
func (r *ring) avg(n int) float64 {
	vs := r.last(n)
	if len(vs) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vs {
		sum += v
	}
	return sum / float64(len(vs))
}

func (r *ring) max(n int) float64 {
	m := 0.0
	for _, v := range r.last(n) {
		m = max(m, v)
	}
	return m
}

// procRate is a process as it's shown: how busy it was over the last round.
type procRate struct {
	PID     int
	Name    string
	CPU     float64 // % of one core: 250 is two and a half cores' worth
	RSS     uint64
	Threads int
}

// vitals is what two rounds of a machine's counters add up to.
type vitals struct {
	CPU                  float64   // 0–100, every core together
	User, System, IOWait float64   // parts of CPU, 0–100 (Linux)
	Steal                float64   // taken by the hypervisor
	Cores                []float64 // each core, 0–100 (Linux)
	LoadPerCore          float64   // the 1-minute load over the cores: 1 is fully busy
	Mem, Swap            float64   // % in use
	NetRx, NetTx         float64   // bytes a second
	DiskRead, DiskWrite  float64
	Top                  []procRate // busiest first
}

// level is how much attention a reading wants: calm, a look, or now.
type level int

const (
	levelOK level = iota
	levelWarn
	levelHot
)

// The lines a percentage crosses (CPU, memory, a disk): warn at 80, hot at 90.
const warnPct, hotPct = 80, 90

// The load per core where it's busy and where things queue: past 1 every
// core is taken and work waits.
const warnLoad, hotLoad = 1.0, 1.5

func pctLevel(p float64) level {
	switch {
	case p >= hotPct:
		return levelHot
	case p >= warnPct:
		return levelWarn
	}
	return levelOK
}

func loadLevel(l float64) level {
	switch {
	case l >= hotLoad:
		return levelHot
	case l >= warnLoad:
		return levelWarn
	}
	return levelOK
}

func tempLevel(c float64) level {
	switch {
	case c >= 90:
		return levelHot
	case c >= 80:
		return levelWarn
	}
	return levelOK
}

// history is a machine's graphs.
type history struct {
	CPU, Load, Mem, Net, NetRx, NetTx, Disk ring
}

// derive works out the vitals from the round before (prev, nil for the
// first) and this one.
func derive(prev, cur *sample) vitals {
	var v vitals
	if cur.MemTotal > 0 {
		v.Mem = 100 * float64(cur.MemTotal-min(cur.MemAvail, cur.MemTotal)) / float64(cur.MemTotal)
	}
	if cur.SwapTotal > 0 {
		v.Swap = 100 * float64(cur.SwapUsed) / float64(cur.SwapTotal)
	}
	if cur.Cores > 0 {
		v.LoadPerCore = cur.Load[0] / float64(cur.Cores)
	}
	if cur.CPUPct >= 0 {
		v.CPU = cur.CPUPct
	}
	// macOS says how busy its processes are; there's nothing to compare.
	if cur.OS == "darwin" {
		for _, p := range cur.Procs {
			v.Top = append(v.Top, procRate{PID: p.PID, Name: p.Name, CPU: p.CPU, RSS: p.RSS, Threads: p.Threads})
		}
		sortTop(v.Top)
	}
	if prev == nil {
		return v
	}
	secs := cur.At.Sub(prev.At).Seconds()
	if secs <= 0 {
		return v
	}
	if cur.OS != "darwin" {
		if busy, part := cpuShare(prev.CPU, cur.CPU); busy >= 0 {
			v.CPU = busy
			v.User, v.System, v.IOWait, v.Steal = part(func(c cpuTimes) uint64 { return c.User + c.Nice }),
				part(func(c cpuTimes) uint64 { return c.System + c.IRQ + c.SoftIRQ }),
				part(func(c cpuTimes) uint64 { return c.IOWait }),
				part(func(c cpuTimes) uint64 { return c.Steal })
		}
		if len(prev.PerCore) == len(cur.PerCore) {
			for i := range cur.PerCore {
				busy, _ := cpuShare(prev.PerCore[i], cur.PerCore[i])
				v.Cores = append(v.Cores, max(0, busy))
			}
		}
		// Ticks are hundredths of a second (USER_HZ, 100 wherever Linux runs):
		// ticks a second are a percentage of one core.
		before := make(map[int]float64, len(prev.Procs))
		for _, p := range prev.Procs {
			before[p.PID] = p.CPU
		}
		for _, p := range cur.Procs {
			was, ok := before[p.PID]
			if !ok || p.CPU < was {
				continue // new since the last round: no rate yet
			}
			v.Top = append(v.Top, procRate{PID: p.PID, Name: p.Name, CPU: (p.CPU - was) / secs, RSS: p.RSS, Threads: p.Threads})
		}
		sortTop(v.Top)
	}
	v.NetRx, v.NetTx = rate(prev.NetRx, cur.NetRx, secs), rate(prev.NetTx, cur.NetTx, secs)
	if cur.HasDiskIO && prev.HasDiskIO {
		v.DiskRead, v.DiskWrite = rate(prev.DiskRead, cur.DiskRead, secs), rate(prev.DiskWrite, cur.DiskWrite, secs)
	}
	return v
}

// cpuShare is how busy a CPU was between two readings (0–100, or -1 if the
// counters didn't move), and a func for the share of any part of its time.
func cpuShare(a, b cpuTimes) (float64, func(func(cpuTimes) uint64) float64) {
	total := float64(b.total()) - float64(a.total())
	if total <= 0 {
		return -1, func(func(cpuTimes) uint64) float64 { return 0 }
	}
	idle := float64(b.idle()) - float64(a.idle())
	part := func(f func(cpuTimes) uint64) float64 {
		return max(0, min(100, 100*(float64(f(b))-float64(f(a)))/total))
	}
	return max(0, min(100, 100*(total-idle)/total)), part
}

// rate is a counter's change a second. A counter that went back (a reboot,
// an interface gone) has no rate.
func rate(a, b uint64, secs float64) float64 {
	if b < a {
		return 0
	}
	return float64(b-a) / secs
}

// sortTop puts the busiest first; at a tie, the one using more memory.
func sortTop(ps []procRate) {
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].CPU != ps[j].CPU {
			return ps[i].CPU > ps[j].CPU
		}
		return ps[i].RSS > ps[j].RSS
	})
}

// record adds a round's vitals to the graphs.
func (h *history) record(v vitals) {
	h.CPU.push(v.CPU)
	h.Load.push(v.LoadPerCore)
	h.Mem.push(v.Mem)
	h.NetRx.push(v.NetRx)
	h.NetTx.push(v.NetTx)
	h.Net.push(v.NetRx + v.NetTx)
	h.Disk.push(v.DiskRead + v.DiskWrite)
}

// fullestDisk is the filesystem nearest to full, or nil if none is known.
func fullestDisk(s *sample) *disk {
	var out *disk
	for i := range s.Disks {
		d := &s.Disks[i]
		if out == nil || diskPct(*d) > diskPct(*out) {
			out = d
		}
	}
	return out
}

// diskPct is how full a filesystem is as df has it: of the space you can
// use, the blocks kept back for root not counted, so 100% is when writes
// fail.
func diskPct(d disk) float64 {
	if d.Used+d.Avail == 0 {
		return 0
	}
	return 100 * float64(d.Used) / float64(d.Used+d.Avail)
}

// health is the most a machine's readings call for, and the one that calls
// for it ("cpu", "memory", "/data", "/data full ~2d"…), "" when calm. A
// filesystem counts both for how full it is and for how soon it will be. CPU and load are taken
// over the last few rounds, so a moment's spike doesn't light it up.
func health(s *sample, v vitals, h *history, fills map[string]time.Duration) (level, string) {
	worst, why := levelOK, ""
	consider := func(l level, what string) {
		if l > worst {
			worst, why = l, what
		}
	}
	consider(pctLevel(h.CPU.avg(3)), "cpu")
	consider(loadLevel(h.Load.avg(3)), "load")
	consider(pctLevel(v.Mem), "memory")
	if s.SwapTotal > 0 && v.Swap >= 50 {
		consider(levelWarn, "swap")
	}
	for _, d := range s.Disks {
		consider(pctLevel(diskPct(d)), d.Mount)
		if left, ok := fills[d.Mount]; ok {
			consider(fillLevel(left), d.Mount+" full "+fillText(left))
		}
	}
	consider(tempLevel(s.TempC), "temperature")
	for _, g := range s.GPUs {
		consider(tempLevel(g.Temp), "gpu")
	}
	return worst, why
}

// uptimeText is how long a machine has been up, briefly: "41d", "3h", "12m".
func uptimeText(d time.Duration) string {
	switch {
	case d <= 0:
		return ""
	case d >= 48*time.Hour:
		return itoa(int(d.Hours()/24)) + "d"
	case d >= time.Hour:
		return itoa(int(d.Hours())) + "h"
	}
	return itoa(int(d.Minutes())) + "m"
}
