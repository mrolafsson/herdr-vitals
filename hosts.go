package main

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// target is a machine to watch, and how it's reached.
type target struct {
	Name   string
	SSH    string // "" for this machine
	Origin string // "this", "herdr" (a saved machine), "config"
	// MachineID is the saved machine's ID in herdr, for the command that
	// sets its ssh right again.
	MachineID string
}

func (t target) local() bool { return t.SSH == "" }

// targets is every machine to watch: this one, herdr's saved machines, then
// the config's, each once.
func targets(cfg config) []target {
	var out []target
	seen := map[string]bool{}
	add := func(t target) {
		key := strings.ToLower(sshHostName(t.SSH))
		if t.local() {
			key = ""
		}
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, t)
	}
	if *cfg.Local {
		name, _ := os.Hostname()
		add(target{Name: shortHost(name), Origin: "this"})
	}
	if *cfg.HerdrMachines {
		for _, m := range herdrMachines() {
			if m.Enabled && m.Target != "" {
				add(target{Name: m.Label, SSH: m.Target, Origin: "herdr", MachineID: m.ID})
			}
		}
	}
	for _, h := range cfg.Hosts {
		if h.SSH != "" {
			add(target{Name: h.Name, SSH: h.SSH, Origin: "config"})
		}
	}
	return out
}

// shortHost is a host name without its domain: "atlas", not
// "atlas.tail1234.ts.net".
func shortHost(name string) string {
	if i := strings.IndexByte(name, '.'); i > 0 {
		return name[:i]
	}
	return name
}

// sshHostName is the host an ssh target names, for showing it: no user, no
// port, no ssh://.
func sshHostName(target string) string {
	host, _ := sshArgs(target)
	if i := strings.LastIndexByte(host, '@'); i >= 0 {
		host = host[i+1:]
	}
	return host
}

// sshArgs is an ssh target as ssh takes it: the destination, and a port if
// the target is a URL with one.
func sshArgs(target string) (dest string, port string) {
	if u, err := url.Parse(target); err == nil && u.Scheme == "ssh" && u.Host != "" {
		dest = u.Hostname()
		if u.User != nil {
			dest = u.User.Username() + "@" + dest
		}
		return dest, u.Port()
	}
	return target, ""
}

// frameMsg is news of a machine: a round of its probe, or that it's gone.
type frameMsg struct {
	host   int
	sample *sample
	status string // "live", "connecting", "down", "auth"
	err    string // what went wrong, when down
	retry  time.Time
}

// probeCommand runs the probe here or, over ssh, there. ssh never asks
// anything (BatchMode): there's nobody to answer it. A connection gone quiet
// is noticed within half a minute.
func probeCommand(ctx context.Context, t target) *exec.Cmd {
	if t.local() {
		return exec.CommandContext(ctx, "sh", "-s")
	}
	dest, port := sshArgs(t.SSH)
	args := []string{"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=10", "-o", "ServerAliveCountMax=3"}
	if port != "" {
		args = append(args, "-p", port)
	}
	// "--" so a target can't be taken for an option; sh reads the probe from
	// ssh's stdin, whatever the login shell there is.
	args = append(args, "--", dest, "sh -s")
	return exec.CommandContext(ctx, "ssh", args...)
}

// watch keeps a machine's probe running until ctx ends, sending each round
// to out (lite: without each process or the temperatures, see script). When the probe ends (the machine went away, ssh was refused) it's
// started again, waiting longer each time it fails in a row, up to a minute.
func watch(ctx context.Context, i int, t target, interval time.Duration, lite bool, out chan<- frameMsg) {
	if lite && t.local() && nativeLite() {
		watchNative(ctx, i, interval, out) // no probe: see native_linux.go
		return
	}
	wait := 2 * time.Second
	for ctx.Err() == nil {
		if !send(ctx, out, frameMsg{host: i, status: "connecting"}) {
			return
		}
		got, errText := runProbe(ctx, i, t, interval, lite, out)
		if ctx.Err() != nil {
			return
		}
		if got {
			wait = 2 * time.Second // it was up: try again soon
		}
		status := "down"
		if isAuthError(errText) {
			status = "auth"
		}
		if !send(ctx, out, frameMsg{host: i, status: status, err: errText, retry: time.Now().Add(wait)}) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, time.Minute)
	}
}

// runProbe runs the probe once, for as long as it lasts: whether any round
// came back, and the last thing it said on stderr.
func runProbe(ctx context.Context, i int, t target, interval time.Duration, lite bool, out chan<- frameMsg) (bool, string) {
	cmd := probeCommand(ctx, t)
	cmd.Stdin = strings.NewReader(script(interval, lite))
	var stderr lockedBuffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, err.Error()
	}
	if err := cmd.Start(); err != nil {
		return false, err.Error()
	}
	got := false
	_ = readFrames(stdout, func(sec map[string][]string) {
		got = true
		send(ctx, out, frameMsg{host: i, status: "live", sample: parseSample(sec, time.Now())})
	})
	werr := cmd.Wait()
	msg := lastLine(stderr.String())
	if msg == "" && werr != nil {
		msg = werr.Error()
	}
	if msg == "" {
		msg = "the connection closed"
	}
	return got, clean(msg, false)
}

func send(ctx context.Context, out chan<- frameMsg, m frameMsg) bool {
	select {
	case out <- m:
		return true
	case <-ctx.Done():
		return false
	}
}

// isAuthError is ssh saying it couldn't log in, which is yours to fix (a key
// to load, a host key to accept), not something that passes.
func isAuthError(s string) bool {
	s = strings.ToLower(s)
	for _, w := range []string{"permission denied", "host key verification failed", "too many authentication failures", "no matching host key"} {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// lockedBuffer is a Buffer that exec's copying and our reading can share.
// Only its last few kilobytes are kept: ssh can go on about a host.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Write(p)
	if b.buf.Len() > 8192 {
		rest := b.buf.Bytes()[b.buf.Len()-4096:]
		b.buf = *bytes.NewBuffer(append([]byte(nil), rest...))
	}
	return len(p), nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
