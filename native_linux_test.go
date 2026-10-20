package main

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Read without the probe, this machine comes out as the probe has it.
func TestNativeMatchesTheProbe(t *testing.T) {
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
