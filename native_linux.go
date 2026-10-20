package main

import (
	"bufio"
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// On Linux, this machine is read straight from /proc by the sampler, with
// no shell: the probe starts a dozen programs a round (cat, df, uname…),
// and always running, that's most of what it costs. What's read is the
// same as the lite probe's, in the same shape, so parseSample takes it
// as it is.

// nativeLite is whether this machine can be read without the probe.
func nativeLite() bool { return true }

// watchNative is watch for this machine, lite, without the probe.
func watchNative(ctx context.Context, i int, interval time.Duration, out chan<- frameMsg) {
	wait := interval / 4 // the second round soon, as the probe has it
	for {
		if !send(ctx, out, frameMsg{host: i, status: "live", sample: parseSample(nativeSections(), time.Now())}) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = interval
	}
}

// nativeSections is a round of the lite probe, read here.
func nativeSections() map[string][]string {
	lines := func(path string) []string {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	}
	host, _ := os.Hostname()
	kernel := strings.TrimSpace(strings.Join(lines("/proc/sys/kernel/osrelease"), ""))
	sec := map[string][]string{
		"os":        {"linux"},
		"host":      {host, kernel, unameArch()},
		"stat":      lines("/proc/stat"),
		"loadavg":   lines("/proc/loadavg"),
		"meminfo":   lines("/proc/meminfo"),
		"uptime":    lines("/proc/uptime"),
		"netdev":    lines("/proc/net/dev"),
		"diskstats": lines("/proc/diskstats"),
		"df":        statfsLines(),
	}
	for _, l := range lines("/etc/os-release") {
		if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
			sec["release"] = []string{strings.Trim(v, `"`)}
		}
	}
	return sec
}

// unameArch is the machine as uname -m says it.
func unameArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	}
	return runtime.GOARCH
}

// remoteFS are filesystems on other machines: asking one that's gone how
// full it is can hang, and it isn't this machine's to fill.
var remoteFS = map[string]bool{"nfs": true, "nfs4": true, "cifs": true, "smbfs": true, "sshfs": true,
	"fuse.sshfs": true, "9p": true, "afs": true, "ceph": true, "glusterfs": true, "fuse.rclone": true}

// statfsLines is df -kP's lines, from the mounts and statfs: what parseDF
// reads.
func statfsLines() []string {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()
	out := []string{"Filesystem 1024-blocks Used Available Capacity Mounted on"}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fl := strings.Fields(sc.Text())
		if len(fl) < 3 || remoteFS[fl[2]] {
			continue
		}
		dev, mount := fl[0], unescapeMount(fl[1])
		if !strings.HasPrefix(dev, "/") || !realFilesystem(dev, mount) {
			continue // not a device: proc, tmpfs, cgroups…
		}
		var st syscall.Statfs_t
		if syscall.Statfs(mount, &st) != nil {
			continue
		}
		bs := uint64(st.Bsize)
		size, avail := st.Blocks*bs/1024, st.Bavail*bs/1024
		used := (st.Blocks - st.Bfree) * bs / 1024
		out = append(out, dev+" "+strconv.FormatUint(size, 10)+" "+strconv.FormatUint(used, 10)+" "+
			strconv.FormatUint(avail, 10)+" - "+mount)
	}
	return out
}

// unescapeMount undoes /proc/mounts' octal escapes: a space is \040.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
