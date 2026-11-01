package main

// The Herdr app's Vitals mod runs this binary as `herdr-vitals app JSON` on
// each machine it shows: one request in, one JSON answer out. A reading is
// this machine now, against the one the mod asked for last (so a reading a
// second apart gives each core's share of that second), with the sampler's
// last minutes to draw from, its disks and when they fill, and how the
// machine's doing in a word. The work is the popup's own code.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// appVersion is this build's version, as herdr-plugin.toml has it (a test
// holds them together): the mod checks it's the one it pinned.
const appVersion = "0.3.0"

type appRequest struct {
	Op      string `json:"op"` // now, machines
	Demo    bool   `json:"demo,omitempty"`
	Machine string `json:"machine,omitempty"` // the demo's: which made-up machine
}

// appDisk is a filesystem as the mod draws it.
type appDisk struct {
	Mount string  `json:"mount"`
	Size  uint64  `json:"size"`
	Used  uint64  `json:"used"`
	Pct   float64 `json:"pct"`
	Level string  `json:"level"` // ok, warn, hot
}

// appReading is a machine now.
type appReading struct {
	Host      string     `json:"host"`
	OS        string     `json:"os"`
	Release   string     `json:"release"`
	Kernel    string     `json:"kernel"`
	Arch      string     `json:"arch"`
	Cores     int        `json:"cores"`
	Uptime    string     `json:"uptime"`
	Warming   bool       `json:"warming"` // no reading before this one to tell CPU from
	CPU       float64    `json:"cpu"`
	User      float64    `json:"user"`
	System    float64    `json:"system"`
	IOWait    float64    `json:"iowait"`
	PerCore   []float64  `json:"perCore"` // each core, 0–100 (Linux)
	Load      [3]float64 `json:"load"`
	LoadCore  float64    `json:"loadPerCore"`
	Mem       float64    `json:"mem"`
	MemTotal  uint64     `json:"memTotal"`
	MemUsed   uint64     `json:"memUsed"`
	Swap      float64    `json:"swap"`
	SwapTotal uint64     `json:"swapTotal"`
	NetRx     float64    `json:"netRx"` // bytes a second
	NetTx     float64    `json:"netTx"`
	DiskRead  float64    `json:"diskRead"`
	DiskWrite float64    `json:"diskWrite"`
	TempC     float64    `json:"tempC"`
	GPUs      []gpu      `json:"gpus"`
	Disks     []appDisk  `json:"disks"`
	History   []point    `json:"history"` // the sampler's last minutes, oldest first
	Level     string     `json:"level"`   // ok, warn, hot: the worst of it
	Why       string     `json:"why"`     // what that is: "cpu", "/data full 2d"
}

func levelWord(l level) string {
	switch l {
	case levelHot:
		return "hot"
	case levelWarn:
		return "warn"
	}
	return "ok"
}

func runApp(ctx context.Context, cfg config, args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("app needs one JSON request")
	}
	var req appRequest
	if err := json.Unmarshal([]byte(args[0]), &req); err != nil {
		return fmt.Errorf("app: %w", err)
	}
	out, err := appAnswer(ctx, cfg, req)
	if err != nil {
		out = map[string]any{"error": err.Error()}
	}
	return json.NewEncoder(stdout).Encode(out)
}

func appAnswer(ctx context.Context, cfg config, req appRequest) (any, error) {
	now := time.Now()
	switch req.Op {
	case "machines":
		// The demo's machines, for its top bar.
		names := []string{}
		for _, d := range demoMachines {
			names = append(names, d.Name)
		}
		return map[string]any{"machines": names, "version": appVersion}, nil
	case "now":
		if req.Demo {
			return demoReading(req.Machine, now)
		}
		if os.MkdirAll(stateDir(), 0o700) == nil {
			_ = os.WriteFile(appAlivePath(), nil, 0o600)
		}
		ensureSampler(cfg)
		return readingNow(ctx, cfg, now)
	}
	return nil, fmt.Errorf("no op %q", req.Op)
}

// appAlivePath is touched by each reading the app asks for, so the sampler
// it started keeps going while it's in use, where there's no herdr TUI.
func appAlivePath() string { return filepath.Join(stateDir(), "app-alive") }

// appKeepsSampler is how long the sampler keeps going after the app's last
// reading.
const appKeepsSampler = 10 * time.Minute

func appReadLately(now time.Time) bool {
	fi, err := os.Stat(appAlivePath())
	return err == nil && now.Sub(fi.ModTime()) < appKeepsSampler
}

// prevPath keeps the reading the mod asked for last, for the next to be
// told against.
func prevPath() string { return filepath.Join(stateDir(), "app-prev.json") }

// readingNow reads this machine once: straight from /proc on Linux, with
// one round of the probe on macOS.
func readingNow(ctx context.Context, cfg config, now time.Time) (*appReading, error) {
	var s *sample
	if nativeLite() {
		s = parseSample(nativeSections(), now)
	} else {
		var err error
		if s, err = probeOnce(ctx); err != nil {
			return nil, err
		}
	}
	var prev *sample
	if data, err := os.ReadFile(prevPath()); err == nil {
		var p sample
		if json.Unmarshal(data, &p) == nil && now.Sub(p.At) < 30*time.Second && now.After(p.At) {
			prev = &p
		}
	}
	if data, err := json.Marshal(s); err == nil && os.MkdirAll(stateDir(), 0o700) == nil {
		_ = os.WriteFile(prevPath()+".tmp", data, 0o600)
		_ = os.Rename(prevPath()+".tmp", prevPath())
	}
	ps := freshRecent(target{Name: "this", Origin: "this"}, cfg.interval(), now)
	return describe(s, prev, ps, nil), nil
}

// describe is a reading, from it and the one before.
func describe(s, prev *sample, ps []point, fills map[string]time.Duration) *appReading {
	v := derive(prev, s)
	var h history
	h.seed(ps)
	h.record(v)
	lv, why := health(s, v, &h, fills)
	r := &appReading{
		Host: clean(s.Hostname, false), OS: s.OS, Release: clean(s.Release, false), Kernel: clean(s.Kernel, false), Arch: s.Arch,
		Cores: s.Cores, Uptime: uptimeText(s.Uptime), Warming: prev == nil && s.OS != "darwin",
		CPU: round1(v.CPU), User: round1(v.User), System: round1(v.System), IOWait: round1(v.IOWait), PerCore: []float64{},
		Load: s.Load, LoadCore: math.Round(v.LoadPerCore*100) / 100,
		Mem: round1(v.Mem), MemTotal: s.MemTotal, MemUsed: s.MemTotal - min(s.MemAvail, s.MemTotal), Swap: round1(v.Swap), SwapTotal: s.SwapTotal,
		NetRx: v.NetRx, NetTx: v.NetTx, DiskRead: v.DiskRead, DiskWrite: v.DiskWrite, TempC: s.TempC, GPUs: s.GPUs,
		Disks: []appDisk{}, History: ps, Level: levelWord(lv), Why: why,
	}
	if r.GPUs == nil {
		r.GPUs = []gpu{}
	}
	if r.History == nil {
		r.History = []point{}
	}
	if len(r.History) > 180 {
		r.History = r.History[len(r.History)-180:]
	}
	for _, c := range v.Cores {
		r.PerCore = append(r.PerCore, round1(c))
	}
	for _, d := range s.Disks {
		// macOS's sealed system volume shares its space with the Data volume,
		// where your files are: that one is the one to watch.
		if s.OS == "darwin" && d.Mount == "/" && hasMount(s, "/System/Volumes/Data") {
			continue
		}
		ad := appDisk{Mount: clean(d.Mount, false), Size: d.Size, Used: d.Used, Pct: round1(diskPct(d)), Level: levelWord(pctLevel(diskPct(d)))}
		r.Disks = append(r.Disks, ad)
	}
	return r
}

func hasMount(s *sample, mount string) bool {
	for _, d := range s.Disks {
		if d.Mount == mount {
			return true
		}
	}
	return false
}

// probeOnce is one round of the probe here: macOS, with no /proc.
func probeOnce(ctx context.Context) (*sample, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-s")
	cmd.Stdin = strings.NewReader(script(2*time.Second, true))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var s *sample
	_ = readFrames(stdout, func(sec map[string][]string) {
		if s == nil {
			s = parseSample(sec, time.Now())
			cancel()
		}
	})
	_ = cmd.Wait()
	if s == nil {
		return nil, errors.New("the probe didn't answer")
	}
	return s, nil
}

// demoReading is a made-up machine now: the demo's own, run from two
// minutes ago to now a second at a time, so its waves carry on from one
// reading to the next.
func demoReading(name string, now time.Time) (any, error) {
	i := 0
	for k, d := range demoMachines {
		if d.Name == name {
			i = k
		}
	}
	d := demoMachines[i]
	if d.down != "" || d.auth != "" {
		return nil, errors.New(d.Name + ": " + d.down + d.auth)
	}
	rng := rand.New(rand.NewSource(now.Unix()*31 + int64(i)))
	st := &demoState{cores: make([]cpuTimes, d.cores), procs: make([]float64, len(d.procs)), t: float64(now.Unix() % 100000)}
	var prev, cur *sample
	var ps []point
	start := now.Add(-120 * time.Second)
	for k := 0; k <= 120; k++ {
		prev, cur = cur, d.round(st, rng, start.Add(time.Duration(k)*time.Second), time.Second)
		if prev != nil {
			ps = append(ps, pointOf(cur.At, derive(prev, cur)))
		}
	}
	return describe(cur, prev, ps[:len(ps)-1], nil), nil
}
