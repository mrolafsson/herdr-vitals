package main

import (
	"strings"
	"testing"
	"time"
)

func TestAMinuteIsItsMean(t *testing.T) {
	var m minute
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix()
	for i, cpu := range []float64{10, 20, 90} { // 12:00:00, :20, :40
		if _, ended := m.add(point{T: base + int64(i*20), CPU: cpu, Mem: 50}); ended {
			t.Fatal("a minute ended inside itself")
		}
	}
	done, ended := m.add(point{T: base + 61, CPU: 5})
	if !ended || done.T != base || done.CPU != 40 || done.CPUMax != 90 || done.Mem != 50 {
		t.Fatalf("%+v %v", done, ended)
	}
}

func TestADayAcrossTheWidth(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	ps := []point{
		{T: now.Add(-23 * time.Hour).Unix(), CPU: 30},
		{T: now.Add(-1 * time.Minute).Unix(), CPU: 70},
		{T: now.Add(-2 * time.Minute).Unix(), CPU: 50},
		{T: now.Add(-48 * time.Hour).Unix(), CPU: 99}, // older than a day: not shown
	}
	cols := dayColumns(ps, 24, now, func(p point) float64 { return p.CPU })
	if cols[0] != -1 || cols[1] != 30 || cols[23] != 70 {
		t.Errorf("%v", cols)
	}
	empty := 0
	for _, c := range cols {
		if c == -1 {
			empty++
		}
	}
	if empty != 22 {
		t.Errorf("%d columns empty, want 22: where the sampler wasn't", empty)
	}
}

// Said once; not again until it's eased well off; then again.
func TestAlertsSayItOnce(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	var said []string
	a := loadAlerts()
	a.notify = func(_, body string) { said = append(said, body) }
	tg := target{Name: "atlas"}
	s := &sample{At: time.Now(), MemTotal: 100 * gib, MemAvail: 5 * gib,
		Disks: []disk{{Mount: "/data", Size: 100 * gib, Used: 95 * gib, Avail: 5 * gib}}}
	v := vitals{Mem: 95}
	fills := map[string]time.Duration{"/data": 6 * time.Hour}
	a.check(tg, s, v, nil, fills)
	a.check(tg, s, v, nil, fills)
	if len(said) != 3 {
		t.Fatalf("said %q", said)
	}
	for _, want := range []string{"memory at 95%", "/data is 95% full", "/data will be full in ~6h"} {
		if !strings.Contains(strings.Join(said, "|"), want) {
			t.Errorf("didn't say %q: %q", want, said)
		}
	}
	// Down to 85%: below the line, but not well below. Nothing new.
	a.check(tg, s, vitals{Mem: 85}, nil, fills)
	// Well below, then back up: said again.
	a.check(tg, s, vitals{Mem: 60}, nil, fills)
	a.check(tg, s, vitals{Mem: 93}, nil, fills)
	if n := strings.Count(strings.Join(said, "|"), "memory at"); n != 2 {
		t.Errorf("memory said %d times: %q", n, said)
	}
	// What's been said outlives a restart.
	b := loadAlerts()
	b.notify = func(_, body string) { said = append(said, "again: "+body) }
	b.check(tg, s, vitals{Mem: 93}, nil, fills)
	if strings.Contains(strings.Join(said, "|"), "again:") {
		t.Errorf("said again after a restart: %q", said)
	}
}

// CPU is said only when it's been flat out the whole two minutes, not for a
// spike, nor before there's two minutes to go by.
func TestCPUAlertsWantTwoMinutes(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	var said []string
	a := loadAlerts()
	a.notify = func(_, body string) { said = append(said, body) }
	now := time.Now()
	var recent []point
	for i := 30; i >= 0; i-- { // a minute: not enough to go by
		recent = append(recent, point{T: now.Add(-time.Duration(i) * 2 * time.Second).Unix(), CPU: 97})
	}
	s := &sample{At: now}
	a.check(target{Name: "x"}, s, vitals{}, recent, nil)
	if len(said) != 0 {
		t.Fatalf("said after a minute: %q", said)
	}
	for i := 90; i > 30; i-- {
		recent = append([]point{{T: now.Add(-time.Duration(i) * 2 * time.Second).Unix(), CPU: 96}}, recent...)
	}
	a.check(target{Name: "x"}, s, vitals{}, recent, nil)
	if len(said) != 1 || !strings.Contains(said[0], "CPU at 9") {
		t.Errorf("said %q", said)
	}
}

func TestOneSamplerAtATime(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	if samplerRunning() {
		t.Fatal("running before it's started")
	}
	held := lockSampler()
	if held == nil {
		t.Fatal("couldn't take the lock")
	}
	if !samplerRunning() || lockSampler() != nil {
		t.Error("a second took the lock")
	}
	held.Close()
	if samplerRunning() {
		t.Error("the lock outlived its holder")
	}
}

// The monitor opens on the sampler's graphs, if they're fresh; stale ones
// would hide a gap.
func TestGraphsStartFromTheSampler(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	tg := target{Name: "x", SSH: "me@x"}
	now := time.Now()
	var ps []point
	for i := 100; i > 0; i-- {
		ps = append(ps, point{T: now.Add(-time.Duration(i) * 2 * time.Second).Unix(), CPU: float64(i)})
	}
	if err := writePoints(tg, "recent", ps); err != nil {
		t.Fatal(err)
	}
	var h history
	h.seed(freshRecent(tg, 2*time.Second, now))
	if len(h.CPU.vals) != 100 || h.CPU.vals[99] != 1 {
		t.Errorf("seeded %d", len(h.CPU.vals))
	}
	if got := freshRecent(tg, 2*time.Second, now.Add(time.Minute)); got != nil {
		t.Error("a minute-old history counted as fresh")
	}
}
