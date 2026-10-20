package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// The sampler reads every machine all the time, lightly (no processes, no
// temperatures: see script), so that:
//
//   - whatever opens next opens on full graphs (store.go),
//   - there's a day of history to look back on (z in the details),
//   - you're told when a machine runs low on CPU, memory or disk (alerts.go),
//   - and disks are written down for when they'll be full (disklog.go)
//     whether the monitor is open or not.
//
// Nothing to look after: herdr's startup hook starts it, and so does opening
// the monitor if it isn't running. A lock keeps it to one. It goes when herdr
// does, and when its program is rebuilt or updated it starts again as the
// new one.

func samplerLock() string { return filepath.Join(stateDir(), "sampler.lock") }

// lockSampler takes the sampler's lock, if no one has it: the file, held
// open for as long as the lock is wanted (Go opens it close-on-exec, so it's
// let go of on a restart). Nil if it's held.
func lockSampler() *os.File {
	if os.MkdirAll(stateDir(), 0o700) != nil {
		return nil
	}
	f, err := os.OpenFile(samplerLock(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return nil
	}
	return f
}

// samplerRunning is whether the sampler has its lock.
func samplerRunning() bool {
	f := lockSampler()
	if f == nil {
		return true
	}
	f.Close()
	return false
}

// ensureSampler starts the sampler, detached, if it isn't running and is
// wanted. Two at once is harmless: the second finds the lock taken and goes.
func ensureSampler(cfg config) {
	if !*cfg.Background || samplerRunning() {
		return
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	log, err := os.OpenFile(filepath.Join(stateDir(), "sampler.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer log.Close()
	cmd := exec.Command(self, "sampler")
	cmd.Stdout, cmd.Stderr = log, log
	// Its own session, so it outlives whoever started it: herdr may end a
	// hook's process group once the hook returns.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if cmd.Start() == nil {
		_ = cmd.Process.Release()
	}
}

// sampled is what the sampler keeps of one machine.
type sampled struct {
	t      target
	prev   *sample
	recent []point
	day    []point
	minute minute
	dlog   *diskLog
}

// runSampler reads every machine until herdr goes (or ctx ends).
func runSampler(ctx context.Context) error {
	lock := lockSampler()
	if lock == nil {
		return nil // one is running
	}
	defer lock.Close()
	self, _ := os.Executable()
	started := modTime(self)
	alerts := loadAlerts()

	for ctx.Err() == nil {
		cfg, _ := loadConfig()
		if !*cfg.Background {
			return nil
		}
		// A round of watching, until the machines to watch may have changed
		// (a minute: herdr's saved machines, the config) or it's time to go.
		reason := sampleFor(ctx, cfg, alerts, time.Minute)
		switch {
		case reason == "herdr gone":
			return nil
		case modTime(self) != started:
			// Rebuilt or updated: become the new program. The lock goes with
			// this one (close-on-exec) and the new one takes it.
			lock.Close()
			return syscall.Exec(self, []string{self, "sampler"}, os.Environ())
		}
	}
	return nil
}

func modTime(path string) time.Time {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// sampleFor watches cfg's machines for about d, keeping what they say, and
// says why it stopped: "" when d is up, "herdr gone" when herdr is.
func sampleFor(ctx context.Context, cfg config, alerts *alerts, d time.Duration) string {
	ctx, cancel := context.WithCancel(ctx)
	// Every watcher gone, its probe ended and waited for, before this
	// returns: else a restart (exec) would leave their shells as zombies.
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	ts := targets(cfg)
	out := make(chan frameMsg, 64)
	hosts := make([]*sampled, len(ts))
	for i, t := range ts {
		hosts[i] = &sampled{t: t, recent: readPoints(t, "recent"), day: readPoints(t, "day"), dlog: openDiskLog(t)}
		wg.Add(1)
		go func() {
			defer wg.Done()
			watch(ctx, i, t, cfg.interval(), true, out)
		}()
	}
	done := time.After(d)
	alive := time.NewTicker(30 * time.Second)
	defer alive.Stop()
	misses := 0
	for {
		select {
		case <-ctx.Done():
			return ""
		case <-done:
			return ""
		case <-alive.C:
			// herdr gone three times running (a minute and a half): go too.
			if herdrCall("ping", nil, nil) != nil {
				if misses++; misses >= 3 {
					return "herdr gone"
				}
			} else {
				misses = 0
			}
		case f := <-out:
			if f.sample != nil {
				hosts[f.host].take(f.sample, cfg, alerts)
			}
		}
	}
}

// take keeps a round of a machine: its point, its minute, its disks, and
// whether it calls for a toast.
func (h *sampled) take(s *sample, cfg config, a *alerts) {
	prev := h.prev
	if prev != nil && s.At.Sub(prev.At) > 3*cfg.interval()+10*time.Second {
		prev = nil
	}
	h.prev = s
	if prev == nil && s.OS != "darwin" {
		return // no rates yet
	}
	v := derive(prev, s)
	p := pointOf(s.At, v)
	h.recent = append(h.recent, p)
	if len(h.recent) > recentKept {
		h.recent = h.recent[len(h.recent)-recentKept:]
	}
	_ = writePoints(h.t, "recent", h.recent)
	if m, ended := h.minute.add(p); ended {
		h.day = append(h.day, m)
		cutoff := s.At.Add(-dayWindow).Unix()
		for len(h.day) > 0 && (h.day[0].T < cutoff || len(h.day) > dayKept) {
			h.day = h.day[1:]
		}
		_ = writePoints(h.t, "day", h.day)
	}
	h.dlog.note(s)
	if *cfg.Alerts {
		a.check(h.t, s, v, h.recent, h.dlog.fills(s.At))
	}
}
