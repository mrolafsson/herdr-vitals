package main

import (
	"context"
	"math"
	"math/rand"
	"time"
)

// The demo is the monitor on made-up machines: to try it, or take
// screenshots without showing your own. Each machine is a few numbers that
// wander, turned into the counters a real probe sends, so everything after
// the probe is the real thing.

type demoMachine struct {
	target
	os, release, kernel, arch string
	cores                     int
	mem, swap                 uint64 // bytes
	cpu, memUse, swapUse      float64
	wander                    float64 // how far CPU strays
	disks                     []disk
	temp                      float64
	gpu                       bool
	down, auth                string // why it's not answering, if it isn't
	procs                     []demoProc
}

type demoProc struct {
	name    string
	share   float64 // of the machine's busy time
	rss     uint64
	threads int
}

const gib = 1 << 30

var demoMachines = []demoMachine{
	{
		target: target{Name: "atlas", Origin: "this"}, os: "linux", release: "Ubuntu 24.04.1 LTS", kernel: "6.8.0-47-generic", arch: "x86_64",
		cores: 16, mem: 64 * gib, swap: 8 * gib, cpu: 38, memUse: 61, swapUse: 2, wander: 14, temp: 58,
		disks: []disk{{"/", 480 * gib, 301 * gib, (480 - 301) * gib}, {"/home", 1800 * gib, 920 * gib, (1800 - 920) * gib}},
		procs: []demoProc{{"claude", .34, 820 << 20, 24}, {"node", .22, 1400 << 20, 11}, {"go", .18, 610 << 20, 18},
			{"herdr", .06, 92 << 20, 9}, {"postgres", .05, 340 << 20, 1}, {"rust-analyzer", .04, 2100 << 20, 15}},
	},
	{
		target: target{Name: "gpu-box", SSH: "gpu-box", Origin: "herdr"}, os: "linux", release: "Debian GNU/Linux 12 (bookworm)", kernel: "6.1.0-26-amd64", arch: "x86_64",
		cores: 32, mem: 256 * gib, swap: 0, cpu: 93, memUse: 77, wander: 5, temp: 71, gpu: true,
		disks: []disk{{"/", 900 * gib, 412 * gib, (900 - 412) * gib}, {"/data", 7200 * gib, 5800 * gib, (7200 - 5800) * gib}},
		procs: []demoProc{{"python3", .82, 48 * gib, 64}, {"dataloader", .09, 6 * gib, 12}, {"nvidia-smi", .01, 40 << 20, 3}},
	},
	{
		target: target{Name: "build-01", SSH: "ci@build-01", Origin: "config"}, os: "linux", release: "Ubuntu 22.04.4 LTS", kernel: "5.15.0-119-generic", arch: "aarch64",
		cores: 8, mem: 16 * gib, swap: 4 * gib, cpu: 55, memUse: 71, swapUse: 14, wander: 30, temp: 0,
		disks: []disk{{"/", 200 * gib, 183 * gib, (200 - 183) * gib}},
		procs: []demoProc{{"cargo", .51, 1900 << 20, 9}, {"rustc", .31, 2600 << 20, 4}, {"dockerd", .04, 180 << 20, 22}},
	},
	{
		target: target{Name: "macbook", SSH: "macbook", Origin: "config"}, os: "darwin", release: "macOS 15.1", kernel: "24.1.0", arch: "arm64",
		cores: 12, mem: 36 * gib, swap: 2 * gib, cpu: 9, memUse: 58, swapUse: 20, wander: 6,
		disks: []disk{{"/System/Volumes/Data", 994 * gib, 611 * gib, (994 - 611) * gib}},
		procs: []demoProc{{"Safari", .3, 1200 << 20, 0}, {"WindowServer", .2, 400 << 20, 0}, {"Ghostty", .1, 300 << 20, 0}},
	},
	{
		target: target{Name: "edge-02", SSH: "edge-02.example.net", Origin: "config"},
		auth:   "edge-02.example.net: Permission denied (publickey).",
	},
}

// demoFills is how soon the made-up machines' disks fill, as their records
// would say: gpu-box's datasets are eating /data.
var demoFills = map[int]map[string]time.Duration{
	1: {"/data": 52 * time.Hour},
	2: {"/": 9 * time.Hour},
}

func demoTargets() []target {
	out := make([]target, len(demoMachines))
	for i, d := range demoMachines {
		out[i] = d.target
	}
	return out
}

// demoState is a made-up machine's counters so far.
type demoState struct {
	cpu, cores []cpuTimes
	procs      []float64
	net, io    [2]uint64
	t          float64
}

// demoWatch is watch for a made-up machine: four minutes of history at once,
// then a round every interval.
func demoWatch(ctx context.Context, i int, t target, interval time.Duration, out chan<- frameMsg) {
	d := demoMachines[i]
	if d.auth != "" {
		send(ctx, out, frameMsg{host: i, status: "auth", err: d.auth, retry: time.Now().Add(42 * time.Second)})
		return
	}
	rng := rand.New(rand.NewSource(int64(i) + 7))
	st := &demoState{cores: make([]cpuTimes, d.cores), procs: make([]float64, len(d.procs))}
	now := time.Now()
	for k := historyLen; k > 0; k-- {
		if !send(ctx, out, frameMsg{host: i, status: "live", sample: d.round(st, rng, now.Add(-time.Duration(k)*interval), interval)}) {
			return
		}
	}
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case at := <-tk.C:
			if !send(ctx, out, frameMsg{host: i, status: "live", sample: d.round(st, rng, at, interval)}) {
				return
			}
		}
	}
}

// round moves the machine on by one interval and says what its probe would.
func (d demoMachine) round(st *demoState, rng *rand.Rand, at time.Time, interval time.Duration) *sample {
	st.t++
	secs := interval.Seconds()
	// A slow swell, a faster ripple and some noise.
	busy := d.cpu + d.wander*(0.6*math.Sin(st.t/23)+0.4*math.Sin(st.t/5.3)) + rng.NormFloat64()*d.wander*0.3
	busy = max(1, min(100, busy))
	s := &sample{At: at, OS: d.os, Hostname: d.Name, Kernel: d.kernel, Arch: d.arch, Release: d.release,
		Cores: d.cores, CPUPct: -1, MemTotal: d.mem, SwapTotal: d.swap, Uptime: time.Duration(41*24*3600+st.t*secs) * time.Second,
		Disks: d.disks, TempC: d.temp + (busy-d.cpu)*0.2}
	mem := max(5, min(99, d.memUse+math.Sin(st.t/40)*3+rng.NormFloat64()*0.4))
	s.MemAvail = uint64(float64(d.mem) * (100 - mem) / 100)
	s.SwapUsed = uint64(float64(d.swap) * d.swapUse / 100)
	load := busy / 100 * float64(d.cores) * (1.05 + 0.1*math.Sin(st.t/31))
	s.Load = [3]float64{load, load * 0.94, load * 0.88}

	// Ticks: a hundred a second a core, the busy share spread unevenly.
	var all cpuTimes
	for c := range st.cores {
		share := max(0, min(1, busy/100*(0.6+0.8*math.Abs(math.Sin(float64(c)*1.7+st.t/9)))))
		ticks := 100 * secs
		st.cores[c].User += uint64(ticks * share * 0.78)
		st.cores[c].System += uint64(ticks * share * 0.18)
		st.cores[c].IOWait += uint64(ticks * share * 0.04)
		st.cores[c].Idle += uint64(ticks * (1 - share))
		c := st.cores[c]
		all.User, all.System, all.IOWait, all.Idle = all.User+c.User, all.System+c.System, all.IOWait+c.IOWait, all.Idle+c.Idle
	}
	s.CPU, s.PerCore = all, append([]cpuTimes(nil), st.cores...)
	if d.os == "darwin" {
		s.CPUPct, s.PerCore = busy, nil
	}

	burst := math.Max(0, math.Sin(st.t/7)) * math.Max(0, math.Sin(st.t/2.3))
	st.net[0] += uint64((400e3 + 6e6*burst + rng.Float64()*200e3) * secs)
	st.net[1] += uint64((90e3 + 1.2e6*burst + rng.Float64()*60e3) * secs)
	// The disks busy at other times: a build, a backup.
	work := math.Max(0, math.Sin(st.t/11+2)) * math.Max(0, math.Sin(st.t/3.1+1))
	st.io[0] += uint64((2e6*work + rng.Float64()*300e3) * secs)
	st.io[1] += uint64((800e3 + 9e6*work*work) * secs)
	s.NetRx, s.NetTx = st.net[0], st.net[1]
	if d.os == "linux" {
		s.DiskRead, s.DiskWrite, s.HasDiskIO = st.io[0], st.io[1], true
	}

	cpuTicks := busy / 100 * float64(d.cores) * 100 * secs
	for k, p := range d.procs {
		st.procs[k] += cpuTicks * p.share * (0.85 + 0.3*rng.Float64())
		cpu := st.procs[k]
		if d.os == "darwin" {
			cpu = busy * float64(d.cores) * p.share
		}
		s.Procs = append(s.Procs, proc{PID: 1200 + k*317, Name: p.name, CPU: cpu, RSS: p.rss, Threads: p.threads})
		s.Threads += max(1, p.threads)
	}
	s.Processes = 380 + len(d.procs)*3
	s.Threads += 1900
	if d.gpu {
		util := max(0, min(100, 96+rng.NormFloat64()*2.5))
		s.GPUs = []gpu{{util, 71200, 81920, 79 + rng.NormFloat64()}, {util - 3, 69000, 81920, 76}}
	}
	return s
}

// demoDay is a made-up machine's last day, a point a minute: busy in the
// working day, quiet at night but for the backup at three, and an hour
// missing in the afternoon, when herdr was restarted.
func demoDay(i int, now time.Time) []point {
	d := demoMachines[i]
	if d.auth != "" {
		return nil
	}
	rng := rand.New(rand.NewSource(int64(i) + 99))
	var out []point
	start := now.Add(-dayWindow).Truncate(time.Minute)
	for t := start; t.Before(now); t = t.Add(time.Minute) {
		hour := float64(t.Hour()) + float64(t.Minute())/60
		if hour >= 15 && hour < 16 && now.Sub(t) > 2*time.Hour {
			continue // the sampler wasn't running
		}
		// Busier from nine to seven, a lull at lunch.
		work := math.Max(0, math.Sin((hour-8)/11*math.Pi))
		if hour >= 12.5 && hour < 13.5 {
			work *= 0.4
		}
		cpu := d.cpu*(0.25+0.9*work) + rng.NormFloat64()*d.wander*0.4
		backup := 0.0
		if hour >= 3 && hour < 3.5 {
			backup = 1
			cpu += 25
		}
		cpu = max(1, min(100, cpu))
		p := point{T: t.Unix(), CPU: cpu, CPUMax: min(100, cpu+rng.Float64()*15), Load: cpu / 100 * 1.05,
			Mem: max(5, min(99, d.memUse-8+10*work+rng.NormFloat64())), Net: (300e3 + 4e6*work*rng.Float64()) * (1 + 6*backup),
			Disk: 500e3 + 3e6*work*rng.Float64() + 40e6*backup}
		p.LoadMax = p.CPUMax / 100 * 1.1
		out = append(out, p)
	}
	return out
}
