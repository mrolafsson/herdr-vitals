package main

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The probe, run here for real: two rounds, and what they add up to is
// believable.
func TestProbeRunsHere(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the Linux probe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-s")
	cmd.Stdin = strings.NewReader(script(300*time.Millisecond, false))
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var rounds []*sample
	_ = readFrames(out, func(sec map[string][]string) {
		rounds = append(rounds, parseSample(sec, time.Now()))
		if len(rounds) == 2 {
			cancel()
		}
	})
	_ = cmd.Wait()
	if len(rounds) < 2 {
		t.Fatalf("got %d rounds", len(rounds))
	}
	s := rounds[1]
	if s.OS != "linux" || s.Cores != runtime.NumCPU() && s.Cores < 1 {
		t.Errorf("os %q, %d cores", s.OS, s.Cores)
	}
	if s.MemTotal == 0 || s.MemAvail == 0 || s.MemAvail > s.MemTotal {
		t.Errorf("memory %d of %d", s.MemAvail, s.MemTotal)
	}
	if s.Processes < 5 || s.Threads < s.Processes {
		t.Errorf("%d processes, %d threads", s.Processes, s.Threads)
	}
	if s.Uptime <= 0 || s.Hostname == "" {
		t.Errorf("uptime %v, host %q", s.Uptime, s.Hostname)
	}
	v := derive(rounds[0], s)
	if v.CPU < 0 || v.CPU > 100 || len(v.Cores) != s.Cores {
		t.Errorf("cpu %.1f, %d cores of %d", v.CPU, len(v.Cores), s.Cores)
	}
	// This test's own shell is among the processes, with its name.
	found := false
	for _, p := range s.Procs {
		if p.Name == "sh" || p.Name == "awk" || p.Name == "cat" {
			found = true
		}
	}
	if !found {
		t.Error("the probe's own processes aren't among the processes")
	}
}

// A process can call itself anything: brackets and spaces too.
func TestProcessNamesSurviveTheirBrackets(t *testing.T) {
	awk := probeScript[strings.Index(probeScript, "awk '"):]
	awk = awk[5:strings.Index(awk, "'\n")]
	stat := "4242 (my (odd) name) S 1 2 3 4 5 6 7 8 9 10 150 50 0 0 20 0 7 0 100 2000 321 18446744073709551615\n"
	cmd := exec.Command("awk", awk)
	cmd.Stdin = strings.NewReader(stat)
	out, err := cmd.Output()
	if err != nil {
		t.Skip("no awk:", err)
	}
	if got, want := strings.TrimSpace(string(out)), "4242 200 321 7 my (odd) name"; got != want {
		t.Errorf("awk made %q, want %q", got, want)
	}
}

func TestDarwinRound(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	sec := map[string][]string{
		"os":      {"darwin"},
		"host":    {"macbook.local", "24.1.0", "arm64"},
		"release": {"macOS 15.1"},
		"sysctl": {"12", "38654705664", "{ 2.50 2.10 1.90 }",
			"{ sec = 1799913600, usec = 0 } Mon Jan 15 08:00:00 2027",
			"total = 2048.00M  used = 512.00M  free = 1536.00M  (encrypted)"},
		"vmstat": {"Mach Virtual Memory Statistics: (page size of 16384 bytes)",
			"Pages free:                               50000.", "Pages active:                            800000.",
			"Pages wired down:                         200000.", "Pages occupied by compressor:              100000."},
		"netstat": {"Name       Mtu   Network       Address            Ipkts Ierrs     Ibytes    Opkts Oerrs     Obytes  Coll",
			"lo0        16384 <Link#1>                         10     0       9999       10     0       9999     0",
			"en0        1500  <Link#11>   aa:bb:cc:dd:ee:ff    100     0     500000      90     0     200000     0",
			"en0        1500  192.168.1     192.168.1.5        100     -     500000      90     -     200000     -"},
		"df": {"Filesystem     1024-blocks      Used Available Capacity  Mounted on",
			"/dev/disk3s1s1   994662584  10000000 400000000     3%    /",
			"/dev/disk3s5     994662584 600000000 400000000    61%    /System/Volumes/Data",
			"devfs                  200       200         0   100%    /dev"},
		"mprocs": {"  1   0.5  20000 /sbin/launchd", "  501  120.0  900000 /Applications/Safari.app/Contents/MacOS/Safari",
			"  777  60.0  1000 /usr/local/bin/my tool"},
		"top": {"Processes: 512 total, 3 running, 509 sleeping, 2803 threads"},
	}
	s := parseSample(sec, at)
	if s.Cores != 12 || s.MemTotal != 38654705664 || s.Load != [3]float64{2.5, 2.1, 1.9} {
		t.Errorf("cores %d mem %d load %v", s.Cores, s.MemTotal, s.Load)
	}
	if s.Uptime != 24*time.Hour {
		t.Errorf("uptime %v", s.Uptime)
	}
	if used := s.MemTotal - s.MemAvail; used != 1_100_000*16384 {
		t.Errorf("used %d", used)
	}
	if s.SwapTotal != 2048<<20 || s.SwapUsed != 512<<20 {
		t.Errorf("swap %d %d", s.SwapUsed, s.SwapTotal)
	}
	if s.NetRx != 500000 || s.NetTx != 200000 {
		t.Errorf("net %d %d", s.NetRx, s.NetTx)
	}
	if len(s.Disks) != 2 || s.Disks[1].Mount != "/System/Volumes/Data" {
		t.Errorf("disks %+v", s.Disks)
	}
	if s.Processes != 512 || s.Threads != 2803 {
		t.Errorf("%d processes %d threads", s.Processes, s.Threads)
	}
	v := derive(nil, s)
	if int(v.CPU) != 15 || v.Top[0].Name != "Safari" || v.Top[1].Name != "my tool" {
		t.Errorf("cpu %.1f, top %+v", v.CPU, v.Top)
	}
}

func TestOnlyRealDisksAndInterfaces(t *testing.T) {
	for name, want := range map[string]bool{"sda": true, "sda1": false, "nvme0n1": true, "nvme0n1p2": false,
		"mmcblk0": true, "mmcblk0p1": false, "loop3": false, "dm-0": false, "md2": false, "vda": true} {
		if wholeDisk(name) != want {
			t.Errorf("wholeDisk(%q) = %v", name, !want)
		}
	}
	for name, want := range map[string]bool{"eth0": true, "enp3s0": true, "lo": false, "docker0": false,
		"veth12ab": false, "tailscale0": false, "wlan0": true, "en0": true, "utun3": false} {
		if realInterface(name) != want {
			t.Errorf("realInterface(%q) = %v", name, !want)
		}
	}
	df := parseDF([]string{
		"Filesystem 1024-blocks Used Available Capacity Mounted on",
		"/dev/md2 458134492 262818180 171991420 61% /",
		"/dev/md2 458134492 262818180 171991420 61% /var/lib/docker/overlay2",
		"tmpfs 32000000 0 32000000 0% /run/user/1000",
		"/dev/sdb1 1000000000 10 999999990 1% /mnt/My Disk",
		"/dev/loop0 2000000 2000000 0 100% /snap/core/1",
	})
	if len(df) != 2 || df[1].Mount != "/mnt/My Disk" {
		t.Fatalf("%+v", df)
	}
	// As df has it: of the space you can use, root's reserve left out.
	if p := diskPct(df[0]); int(p+0.5) != 60 {
		t.Errorf("/ at %.1f%%", p)
	}
}

func TestRates(t *testing.T) {
	t0 := time.Unix(1000, 0)
	a := &sample{At: t0, OS: "linux", CPU: cpuTimes{User: 100, Idle: 900}, NetRx: 1000,
		Procs: []proc{{PID: 1, Name: "busy", CPU: 0}, {PID: 2, Name: "gone", CPU: 5}}}
	b := &sample{At: t0.Add(2 * time.Second), OS: "linux", CPU: cpuTimes{User: 250, Idle: 950, IOWait: 0}, NetRx: 5000,
		Procs: []proc{{PID: 1, Name: "busy", CPU: 300}, {PID: 3, Name: "new", CPU: 999}}}
	v := derive(a, b)
	if v.CPU != 75 || v.NetRx != 2000 {
		t.Errorf("cpu %.1f net %.0f", v.CPU, v.NetRx)
	}
	// 300 ticks in 2 seconds: a core and a half. The newcomer has no rate yet.
	if len(v.Top) != 1 || v.Top[0].CPU != 150 {
		t.Errorf("top %+v", v.Top)
	}
	// A counter that went back (a reboot) is no rate, not a huge one.
	b.NetRx = 10
	if v := derive(a, b); v.NetRx != 0 {
		t.Errorf("net %.0f after a reset", v.NetRx)
	}
}

func TestWhenTheDiskWillBeFull(t *testing.T) {
	now := time.Now()
	var ps []diskPoint
	// 1 GiB more an hour for six hours, 48 GiB left: two days.
	for h := 0; h <= 6; h++ {
		ps = append(ps, diskPoint{now.Add(time.Duration(h-6) * time.Hour), "/data", float64(100+h) * gib, float64(54-h) * gib})
	}
	left, ok := timeToFull(ps)
	if !ok || left < 47*time.Hour || left > 49*time.Hour {
		t.Errorf("full in %v (%v)", left, ok)
	}
	if fillLevel(left) != levelWarn || fillText(left) != "~2d" {
		t.Errorf("level %v text %q", fillLevel(left), fillText(left))
	}
	// Shrinking, or too little history, says nothing.
	if _, ok := timeToFull(ps[:3]); ok {
		t.Error("a trend from two hours of three points")
	}
	for i := range ps {
		ps[i].used = float64(200-i) * gib
	}
	if _, ok := timeToFull(ps); ok {
		t.Error("a shrinking disk will be full")
	}
}

func TestDiskLogRoundTrips(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	tg := target{Name: "x", SSH: "me@x:22"}
	l := openDiskLog(tg)
	start := time.Now().Add(-5 * time.Hour)
	for i := 0; i < 50; i++ {
		l.note(&sample{At: start.Add(time.Duration(i) * 6 * time.Minute),
			Disks: []disk{{Mount: "/srv/my data", Size: 100 * gib, Used: uint64(40+i/5) * gib, Avail: uint64(60-i/5) * gib}}})
	}
	again := openDiskLog(tg)
	if len(again.points) != 50 || again.points[0].mount != "/srv/my data" {
		t.Fatalf("read back %d points: %+v", len(again.points), again.points[:1])
	}
	if _, ok := again.fills(time.Now())["/srv/my data"]; !ok {
		t.Error("no projection from five hours of a filling disk")
	}
}

func TestSSHTargets(t *testing.T) {
	for in, want := range map[string][2]string{
		"gpu-box":                {"gpu-box", ""},
		"me@10.0.0.4":            {"me@10.0.0.4", ""},
		"ssh://me@host.lan:2222": {"me@host.lan", "2222"},
		"ssh://host.lan":         {"host.lan", ""},
	} {
		if d, p := sshArgs(in); d != want[0] || p != want[1] {
			t.Errorf("sshArgs(%q) = %q %q", in, d, p)
		}
	}
	if sshHostName("ssh://me@host.lan:2222") != "host.lan" || sshHostName("me@box") != "box" {
		t.Error("sshHostName")
	}
	cfg := withDefaults(config{Hosts: []hostConfig{{SSH: "me@box"}}})
	if cfg.Hosts[0].Name != "box" || cfg.interval() != 2*time.Second {
		t.Errorf("%+v", cfg)
	}
	if !isAuthError("me@box: Permission denied (publickey).") || isAuthError("Connection timed out") {
		t.Error("isAuthError")
	}
}

// Read without the probe, this machine comes out as the probe has it.
func TestNativeMatchesTheProbe(t *testing.T) {
	if !nativeLite() {
		t.Skip("Linux only")
	}
	n := parseSample(nativeSections(), time.Now())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-s")
	cmd.Stdin = strings.NewReader(script(time.Second, true))
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var p *sample
	_ = readFrames(out, func(sec map[string][]string) {
		if p == nil {
			p = parseSample(sec, time.Now())
			cancel()
		}
	})
	_ = cmd.Wait()
	if p == nil {
		t.Fatal("no round from the probe")
	}
	if n.Hostname != p.Hostname || n.Kernel != p.Kernel || n.Arch != p.Arch || n.Release != p.Release || n.Cores != p.Cores || n.MemTotal != p.MemTotal {
		t.Errorf("native %q %q %q %q %d %d\nprobe  %q %q %q %q %d %d", n.Hostname, n.Kernel, n.Arch, n.Release, n.Cores, n.MemTotal,
			p.Hostname, p.Kernel, p.Arch, p.Release, p.Cores, p.MemTotal)
	}
	mounts := func(s *sample) string {
		var m []string
		for _, d := range s.Disks {
			m = append(m, d.Mount+" "+strconv.Itoa(int(diskPct(d))))
		}
		return strings.Join(m, ", ")
	}
	if mounts(n) != mounts(p) {
		t.Errorf("filesystems: native %s, df %s", mounts(n), mounts(p))
	}
	if unescapeMount(`/mnt/My\040Disk`) != "/mnt/My Disk" {
		t.Error("unescapeMount")
	}
}
