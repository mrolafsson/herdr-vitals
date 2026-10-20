package main

import (
	"bufio"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The probe is a small POSIX shell script, the same one here and on every
// server: nothing to install, only sh, cat and awk. It loops, printing what
// the kernel says in sections ("@@stat", "@@meminfo"…) and "@@end" after
// each round, and sleeps between rounds. Over ssh it's one session, held
// open, so a round costs a few kilobytes and no new connection.
//
// The second round follows the first after a quarter of the interval, so CPU
// (the difference between two) is known almost at once.
//
// It sends raw counters (CPU ticks, bytes received), not rates: rates are
// the difference between two rounds, worked out here (see vitals.go).
const probeScript = `export LC_ALL=C
i=__INTERVAL__
first=__FIRST__
lite=__LITE__
case $(uname -s) in Darwin) os=darwin ;; *) os=linux ;; esac
n=0
while :; do
echo "@@os $os"
echo @@host; uname -n; uname -r; uname -m
if [ $os = linux ]; then
	echo @@release; (. /etc/os-release 2>/dev/null && echo "$PRETTY_NAME")
	echo @@stat; cat /proc/stat
	echo @@loadavg; cat /proc/loadavg
	echo @@meminfo; cat /proc/meminfo
	echo @@uptime; cat /proc/uptime
	echo @@netdev; cat /proc/net/dev
	echo @@diskstats; cat /proc/diskstats
	echo @@df; df -kP 2>/dev/null
	[ $lite = 1 ] || { echo @@temp; cat /sys/class/hwmon/hwmon*/temp*_input /sys/class/thermal/thermal_zone*/temp 2>/dev/null; }
	echo @@gpu; nvidia-smi --query-gpu=utilization.gpu,memory.used,memory.total,temperature.gpu --format=csv,noheader,nounits 2>/dev/null
	# A process's name is in brackets and can hold anything, spaces and
	# brackets too: what follows its last ") " is its numbered fields.
	[ $lite = 1 ] || echo @@procs; [ $lite = 1 ] || cat /proc/[0-9]*/stat 2>/dev/null | awk '{ s = $0; o = index(s, "("); r = s; sub(/.*\) /, "", r); c = substr(s, o + 1, length(s) - length(r) - o - 2); split(r, f, " "); print $1, f[12] + f[13], f[22], f[18], c }'
else
	echo @@release; echo "macOS $(sw_vers -productVersion 2>/dev/null)"
	echo @@sysctl; sysctl -n hw.ncpu hw.memsize vm.loadavg kern.boottime vm.swapusage 2>/dev/null
	echo @@vmstat; vm_stat 2>/dev/null
	echo @@netstat; netstat -ibn 2>/dev/null
	echo @@df; df -kP 2>/dev/null
	echo @@mprocs; if [ $lite = 1 ]; then ps -Ao %cpu= 2>/dev/null; else ps -Ao pid=,%cpu=,rss=,comm= 2>/dev/null; fi
	# top is slow to start: the thread count only every fifth round.
	if [ $lite = 0 ] && [ $((n % 5)) = 0 ]; then echo @@top; top -l 1 -n 0 -s 0 2>/dev/null | grep '^Processes:'; fi
fi
echo @@end
n=$((n + 1))
if [ $n = 1 ]; then sleep $first; else sleep $i; fi
done
`

// script is the probe with its interval filled in. The second round comes
// sooner, a quarter of the interval after the first: CPU is the difference
// between two, and there's no need to wait a whole interval for the first.
// A lite probe, for the sampler that's always running, leaves out what's
// costly to read and only shown when you look: each process, and the
// temperature sensors.
func script(interval time.Duration, lite bool) string {
	secs := func(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) }
	l := "0"
	if lite {
		l = "1"
	}
	return strings.NewReplacer("__INTERVAL__", secs(interval), "__FIRST__", secs(interval/4), "__LITE__", l).Replace(probeScript)
}

// cpuTimes is one line of /proc/stat, in clock ticks.
type cpuTimes struct {
	User, Nice, System, Idle, IOWait, IRQ, SoftIRQ, Steal uint64
}

func (c cpuTimes) total() uint64 {
	return c.User + c.Nice + c.System + c.Idle + c.IOWait + c.IRQ + c.SoftIRQ + c.Steal
}

// idle is the time the CPU had nothing to run, waiting on a disk included.
func (c cpuTimes) idle() uint64 { return c.Idle + c.IOWait }

type disk struct {
	Mount             string
	Size, Used, Avail uint64 // bytes
}

type gpu struct {
	Util, MemUsed, MemTotal, Temp float64 // %, MiB, MiB, °C
}

// proc is one process. CPU is its CPU time so far, in clock ticks, on Linux;
// on macOS, where there are no counters to compare, ps's own percentage.
type proc struct {
	PID     int
	Name    string
	CPU     float64
	RSS     uint64 // bytes
	Threads int
}

// sample is one round of the probe: what the machine said, as it said it.
type sample struct {
	At                     time.Time
	OS                     string // linux, darwin
	Hostname, Kernel, Arch string
	Release                string // "Ubuntu 24.04.1 LTS", "macOS 15.1"
	Cores                  int

	CPU                 cpuTimes   // all cores (Linux)
	PerCore             []cpuTimes // (Linux)
	CPUPct              float64    // all cores, 0–100, said outright (macOS); -1 if not
	Load                [3]float64
	Uptime              time.Duration
	MemTotal            uint64 // bytes
	MemAvail            uint64
	SwapTotal, SwapUsed uint64
	NetRx, NetTx        uint64 // bytes so far, every real interface
	DiskRead, DiskWrite uint64 // bytes so far, every whole disk
	HasDiskIO           bool
	Disks               []disk
	TempC               float64 // the hottest sensor; 0 if none
	GPUs                []gpu
	Procs               []proc
	Processes, Threads  int
}

// readFrames reads the probe's output, calling fn with each round as its
// sections: name to lines.
func readFrames(r io.Reader, fn func(map[string][]string)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	sections := map[string][]string{}
	current := ""
	for sc.Scan() {
		line := sc.Text()
		if name, ok := strings.CutPrefix(line, "@@"); ok {
			name, arg, _ := strings.Cut(name, " ")
			switch name {
			case "end":
				fn(sections)
				sections, current = map[string][]string{}, ""
			case "os":
				sections["os"] = []string{arg}
				current = ""
			default:
				current = name
				sections[name] = nil
			}
			continue
		}
		if current != "" {
			sections[current] = append(sections[current], line)
		}
	}
	return sc.Err()
}

// parseSample makes a sample of one round's sections.
func parseSample(sec map[string][]string, at time.Time) *sample {
	s := &sample{At: at, CPUPct: -1}
	if os := sec["os"]; len(os) > 0 {
		s.OS = os[0]
	}
	if h := sec["host"]; len(h) >= 3 {
		s.Hostname, s.Kernel, s.Arch = h[0], h[1], h[2]
	}
	if r := sec["release"]; len(r) > 0 {
		s.Release = strings.TrimSpace(r[0])
	}
	s.Disks = parseDF(sec["df"])
	if s.OS == "darwin" {
		parseDarwin(s, sec)
	} else {
		parseLinux(s, sec)
	}
	sanitize(s)
	return s
}

func parseLinux(s *sample, sec map[string][]string) {
	for _, line := range sec["stat"] {
		f := strings.Fields(line)
		if len(f) < 9 || !strings.HasPrefix(f[0], "cpu") {
			continue
		}
		var t cpuTimes
		for i, p := range []*uint64{&t.User, &t.Nice, &t.System, &t.Idle, &t.IOWait, &t.IRQ, &t.SoftIRQ, &t.Steal} {
			*p = atou(f[i+1])
		}
		if f[0] == "cpu" {
			s.CPU = t
		} else {
			s.PerCore = append(s.PerCore, t)
		}
	}
	s.Cores = len(s.PerCore)
	if l := sec["loadavg"]; len(l) > 0 {
		f := strings.Fields(l[0])
		for i := 0; i < 3 && i < len(f); i++ {
			s.Load[i] = atof(f[i])
		}
	}
	mem := map[string]uint64{}
	for _, line := range sec["meminfo"] {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) > 0 {
			mem[k] = atou(f[0]) * 1024
		}
	}
	s.MemTotal = mem["MemTotal"]
	s.MemAvail = mem["MemAvailable"]
	if _, ok := mem["MemAvailable"]; !ok { // kernels before 3.14
		s.MemAvail = mem["MemFree"] + mem["Buffers"] + mem["Cached"]
	}
	s.SwapTotal = mem["SwapTotal"]
	if mem["SwapFree"] <= s.SwapTotal {
		s.SwapUsed = s.SwapTotal - mem["SwapFree"]
	}
	if u := sec["uptime"]; len(u) > 0 {
		if f := strings.Fields(u[0]); len(f) > 0 {
			s.Uptime = time.Duration(atof(f[0]) * float64(time.Second))
		}
	}
	for _, line := range sec["netdev"] {
		name, rest, ok := strings.Cut(line, ":")
		f := strings.Fields(rest)
		if !ok || len(f) < 9 || !realInterface(strings.TrimSpace(name)) {
			continue
		}
		s.NetRx += atou(f[0])
		s.NetTx += atou(f[8])
	}
	for _, line := range sec["diskstats"] {
		f := strings.Fields(line)
		if len(f) < 10 || !wholeDisk(f[2]) {
			continue
		}
		s.DiskRead += atou(f[5]) * 512
		s.DiskWrite += atou(f[9]) * 512
		s.HasDiskIO = true
	}
	for _, line := range sec["temp"] {
		// Millidegrees. Some sensors report nonsense: none is believed above 150°.
		if t := atof(strings.TrimSpace(line)) / 1000; t > s.TempC && t < 150 {
			s.TempC = t
		}
	}
	for _, line := range sec["gpu"] {
		f := strings.Split(line, ",")
		if len(f) < 4 {
			continue
		}
		s.GPUs = append(s.GPUs, gpu{atof(f[0]), atof(f[1]), atof(f[2]), atof(f[3])})
	}
	for _, line := range sec["procs"] {
		f := strings.SplitN(line, " ", 5)
		if len(f) < 5 {
			continue
		}
		p := proc{PID: atoi(f[0]), CPU: atof(f[1]), Threads: atoi(f[3]), Name: f[4]}
		p.RSS = atou(f[2]) * pageSize(s)
		s.Procs = append(s.Procs, p)
		s.Threads += p.Threads
	}
	s.Processes = len(s.Procs)
}

// pageSize is the size of a memory page: 4 KiB but on 64 KiB-page arm
// kernels, which say so in their release ("…-64k").
func pageSize(s *sample) uint64 {
	if strings.HasSuffix(s.Kernel, "64k") {
		return 64 * 1024
	}
	return 4096
}

var (
	loadRE    = regexp.MustCompile(`^\{\s*([\d.]+)\s+([\d.]+)\s+([\d.]+)`)
	bootRE    = regexp.MustCompile(`sec = (\d+)`)
	swapRE    = regexp.MustCompile(`total = ([\d.]+)([KMGT])\s+used = ([\d.]+)([KMGT])`)
	pageRE    = regexp.MustCompile(`page size of (\d+) bytes`)
	threadsRE = regexp.MustCompile(`(\d+) total.*?(\d+) threads`)
	// nvme0n1p2, mmcblk0p1: a partition of a disk whose name ends in a digit.
	partitionRE = regexp.MustCompile(`p\d+$`)
)

func parseDarwin(s *sample, sec map[string][]string) {
	ints := 0
	for _, line := range sec["sysctl"] {
		line = strings.TrimSpace(line)
		switch {
		case loadRE.MatchString(line):
			m := loadRE.FindStringSubmatch(line)
			s.Load = [3]float64{atof(m[1]), atof(m[2]), atof(m[3])}
		case bootRE.MatchString(line):
			boot := time.Unix(int64(atou(bootRE.FindStringSubmatch(line)[1])), 0)
			s.Uptime = s.At.Sub(boot)
		case swapRE.MatchString(line):
			m := swapRE.FindStringSubmatch(line)
			s.SwapTotal, s.SwapUsed = sized(m[1], m[2]), sized(m[3], m[4])
		case isDigits(line):
			// hw.ncpu, then hw.memsize: in the order they were asked for.
			if ints == 0 {
				s.Cores = atoi(line)
			} else {
				s.MemTotal = atou(line)
			}
			ints++
		}
	}
	page := uint64(4096)
	pages := map[string]uint64{}
	for _, line := range sec["vmstat"] {
		if m := pageRE.FindStringSubmatch(line); m != nil {
			page = atou(m[1])
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			pages[strings.TrimSpace(k)] = atou(strings.TrimSuffix(strings.TrimSpace(v), "."))
		}
	}
	// In use as Activity Monitor counts it, near enough: what's active, wired
	// down, or squeezed into the compressor. The rest can be had back.
	used := (pages["Pages active"] + pages["Pages wired down"] + pages["Pages occupied by compressor"]) * page
	if used <= s.MemTotal {
		s.MemAvail = s.MemTotal - used
	}
	for _, line := range sec["netstat"] {
		f := strings.Fields(line)
		// The interface's own row (its network <Link#n>) has its totals.
		if len(f) < 10 || !strings.HasPrefix(f[2], "<Link") || !realInterface(f[0]) {
			continue
		}
		s.NetRx += atou(f[len(f)-5])
		s.NetTx += atou(f[len(f)-2])
	}
	total := 0.0
	for _, line := range sec["mprocs"] {
		f := strings.Fields(line)
		if len(f) == 1 { // a lite probe's: only how busy
			total += atof(f[0])
			continue
		}
		if len(f) < 4 {
			continue
		}
		// comm is the program's path, which may have spaces in it.
		name := path.Base(strings.Join(f[3:], " "))
		p := proc{PID: atoi(f[0]), CPU: atof(f[1]), RSS: atou(f[2]) * 1024, Name: name}
		total += p.CPU
		s.Procs = append(s.Procs, p)
	}
	s.Processes = len(s.Procs)
	if s.Cores > 0 {
		s.CPUPct = min(100, total/float64(s.Cores))
	}
	for _, line := range sec["top"] {
		if m := threadsRE.FindStringSubmatch(line); m != nil {
			s.Processes, s.Threads = atoi(m[1]), atoi(m[2])
		}
	}
}

// parseDF reads `df -kP`: the filesystems worth showing, each once.
func parseDF(lines []string) []disk {
	var out []disk
	seen := map[string]bool{}
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) < 6 || f[0] == "Filesystem" {
			continue
		}
		// The mount point is last and may have spaces in it.
		dev, mount := f[0], strings.Join(f[5:], " ")
		size, used, avail := atou(f[1])*1024, atou(f[2])*1024, atou(f[3])*1024
		if size < 1<<30 || seen[dev] || !realFilesystem(dev, mount) {
			continue
		}
		seen[dev] = true
		out = append(out, disk{Mount: mount, Size: size, Used: used, Avail: avail})
	}
	return out
}

// realFilesystem leaves out what's in memory, packaged or the system's own
// plumbing: what fills up there isn't yours to clear.
func realFilesystem(dev, mount string) bool {
	switch dev {
	case "tmpfs", "devtmpfs", "overlay", "udev", "none", "shm", "efivarfs", "devfs", "map", "squashfs":
		return false
	}
	if strings.HasPrefix(dev, "/dev/loop") || strings.HasPrefix(dev, "map ") {
		return false
	}
	for _, p := range []string{"/snap", "/run", "/sys", "/proc", "/dev", "/boot", "/var/lib/docker", "/private/var/vm"} {
		if mount == p || strings.HasPrefix(mount, p+"/") {
			return false
		}
	}
	// macOS's system volumes, but for the one your files are on.
	if strings.HasPrefix(mount, "/System/Volumes/") && mount != "/System/Volumes/Data" {
		return false
	}
	return true
}

// realInterface leaves out loopback and the virtual interfaces containers,
// VMs and VPNs make: what goes over a tunnel goes over a real interface too,
// and would be counted twice.
func realInterface(name string) bool {
	for _, p := range []string{"lo", "veth", "docker", "br-", "virbr", "cni", "flannel", "cali", "vnet",
		"tailscale", "wg", "tun", "utun", "awdl", "llw", "bridge", "gif", "stf", "anpi", "ap", "zt"} {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	return name != ""
}

// wholeDisk is a disk itself, not a partition of one or a device made of
// others, so nothing is counted twice.
func wholeDisk(name string) bool {
	for _, p := range []string{"loop", "ram", "zram", "dm-", "md", "sr", "fd", "nbd"} {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	if strings.HasPrefix(name, "nvme") || strings.HasPrefix(name, "mmcblk") {
		return !partitionRE.MatchString(name)
	}
	return name != "" && (name[len(name)-1] < '0' || name[len(name)-1] > '9')
}

func sized(n, unit string) uint64 {
	mult := map[string]float64{"K": 1 << 10, "M": 1 << 20, "G": 1 << 30, "T": 1 << 40}[unit]
	return uint64(atof(n) * mult)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func atou(s string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	return n
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func atof(s string) float64 {
	n, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return n
}
