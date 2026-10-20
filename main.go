// herdr-vitals: how your machines are doing, at a glance, in herdr: CPU,
// load, memory, disks, network and what's busiest, for this machine and
// every one you work on over ssh.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const usage = `herdr-vitals — how your machines are doing, in herdr

  action open      what the herdr action runs: opens the popup
  action pane      what the herdr action runs: opens the monitor in a pane
                   beside yours, or closes it if it's open
  action demo      what the herdr action runs: the popup on fictional machines
  picker [--demo]  the popup itself
  pane [--demo]    the monitor pane itself
  list             one reading of every machine, as text
  sampler          reads every machine in the background, for full graphs on
                   opening, a day's history and toasts when one runs low
                   (herdr starts it; --detach starts it if it isn't running)
  stream           this machine's CPU, memory and load, a JSON line a
                   second, for as long as it's read: what herdr-recap's
                   header draws
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "herdr-vitals:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "herdr-vitals: using the defaults:", err)
		if args[0] == "action" {
			notify("Vitals", "Using the defaults: "+err.Error())
		}
	}
	demo := os.Getenv("HERDR_VITALS_DEMO") == "1" || (len(args) > 1 && args[1] == "--demo")
	switch args[0] {
	case "action":
		if len(args) < 2 {
			return errors.New("action needs a name")
		}
		return runAction(args[1])
	case "picker", "popup":
		if err := runUI(ctx, cfg, modePopup, demo); err != nil {
			return err
		}
		return finishSwap()
	case "stream":
		ensureSampler(cfg)
		return stream(ctx, os.Stdout)
	case "sampler":
		if len(args) > 1 && args[1] == "--detach" {
			ensureSampler(cfg)
			return nil
		}
		return runSampler(ctx)
	case "pane":
		return runUI(ctx, cfg, modePane, demo)
	case "list":
		return debugList(ctx, cfg)
	default:
		fmt.Print(usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// The popup's size, as herdr-plugin.toml has it: big, the cards want room.
const popupWidth, popupHeight = "90%", "85%"

func runAction(name string) error {
	if cfg, _ := loadConfig(); name != "demo" {
		ensureSampler(cfg)
	}
	switch name {
	case "open", "demo":
		env := map[string]string{}
		if name == "demo" {
			env["HERDR_VITALS_DEMO"] = "1"
		}
		err := openPopup("picker", popupWidth, popupHeight, env)
		if isHerdrCode(err, "ui_busy") {
			// herdr shows one popup at a time. If it's ours, left open on
			// another client say, end it and open it here.
			if !endOwnPopup() {
				notify("Vitals", "Another popup is open: close it (esc) and try again.")
				return err
			}
			err = openPopup("picker", popupWidth, popupHeight, env)
		}
		if err != nil {
			notify("Vitals", "Couldn't open the popup: "+err.Error())
		}
		return err
	case "pane":
		return togglePane()
	}
	return fmt.Errorf("unknown action %q", name)
}

// paneFile holds the monitor pane's ID while it's open, so the same key
// closes it again.
func paneFile() string { return filepath.Join(stateDir(), "pane.id") }

// togglePane opens the monitor beside the pane you're in, or closes it if
// it's open (in any tab: there's one monitor).
func togglePane() error {
	if data, err := os.ReadFile(paneFile()); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" && paneExists(id) {
			os.Remove(paneFile())
			if closePane(id) {
				return nil
			}
		}
	}
	err := openPane("pane", nil)
	if err != nil {
		notify("Vitals", "Couldn't open the monitor: "+err.Error())
	}
	return err
}

// notePane records the monitor's pane, and returns a func that forgets it
// once it closes, unless another has taken its place.
func notePane() func() {
	id := os.Getenv("HERDR_PANE_ID")
	if id == "" || os.MkdirAll(stateDir(), 0o700) != nil || os.WriteFile(paneFile(), []byte(id), 0o600) != nil {
		return func() {}
	}
	return func() {
		if data, err := os.ReadFile(paneFile()); err == nil && strings.TrimSpace(string(data)) == id {
			os.Remove(paneFile())
		}
	}
}

// debugList reads every machine twice, a round apart, and prints what it
// makes of them: for checking the probe against top, free and df.
func debugList(ctx context.Context, cfg config) error {
	ts := targets(cfg)
	out := make(chan frameMsg, 64)
	ctx, cancel := context.WithTimeout(ctx, cfg.interval()*2+15*time.Second)
	defer cancel()
	for i, t := range ts {
		go watch(ctx, i, t, cfg.interval(), false, out)
	}
	prev := make([]*sample, len(ts))
	done := map[int]bool{}
	for len(done) < len(ts) {
		select {
		case <-ctx.Done():
			return nil
		case m := <-out:
			t := ts[m.host]
			switch {
			case done[m.host]:
			case m.status == "down" || m.status == "auth":
				fmt.Printf("%s (%s): %s: %s\n\n", t.Name, t.SSH, m.status, m.err)
				done[m.host] = true
			case m.sample != nil && prev[m.host] == nil:
				prev[m.host] = m.sample
			case m.sample != nil:
				printVitals(t, m.sample, derive(prev[m.host], m.sample))
				done[m.host] = true
			}
		}
	}
	return nil
}

func printVitals(t target, s *sample, v vitals) {
	fmt.Printf("%s  %s · %s · %s · %d cores · up %s\n", t.Name, s.Release, s.Kernel, s.Arch, s.Cores, uptimeText(s.Uptime))
	fmt.Printf("  cpu %.1f%% (user %.1f sys %.1f iowait %.1f steal %.1f)  load %.2f %.2f %.2f (%.2f a core)\n",
		v.CPU, v.User, v.System, v.IOWait, v.Steal, s.Load[0], s.Load[1], s.Load[2], v.LoadPerCore)
	fmt.Printf("  memory %.1f%% of %s  swap %.1f%% of %s\n", v.Mem, bytesText(s.MemTotal), v.Swap, bytesText(s.SwapTotal))
	fmt.Printf("  net ↓%s/s ↑%s/s  disk r %s/s w %s/s  %d processes, %d threads  %.0f°C\n",
		bytesText(uint64(v.NetRx)), bytesText(uint64(v.NetTx)), bytesText(uint64(v.DiskRead)), bytesText(uint64(v.DiskWrite)),
		s.Processes, s.Threads, s.TempC)
	for _, d := range s.Disks {
		fmt.Printf("  %-20s %5.1f%% of %s\n", d.Mount, diskPct(d), bytesText(d.Size))
	}
	for _, g := range s.GPUs {
		fmt.Printf("  gpu %.0f%%  %.0f/%.0f MiB  %.0f°C\n", g.Util, g.MemUsed, g.MemTotal, g.Temp)
	}
	for i, p := range v.Top {
		if i == 5 {
			break
		}
		fmt.Printf("  %6d %6.1f%% %8s %3d  %s\n", p.PID, p.CPU, bytesText(p.RSS), p.Threads, p.Name)
	}
	fmt.Println()
}

// popupFile holds the running popup's process, so an open that finds one
// already up can end it. A popup lasts as long as its command.
func popupFile() string { return filepath.Join(stateDir(), "popup.pid") }

// notePopup records this process as the popup, and returns a func that
// forgets it again, unless a newer popup has replaced it.
func notePopup() func() {
	pid := strconv.Itoa(os.Getpid())
	if os.MkdirAll(stateDir(), 0o700) != nil || os.WriteFile(popupFile(), []byte(pid), 0o600) != nil {
		return func() {}
	}
	return func() {
		if data, err := os.ReadFile(popupFile()); err == nil && strings.TrimSpace(string(data)) == pid {
			os.Remove(popupFile())
		}
	}
}

// endOwnPopup ends this plugin's popup if one is running, and waits for it
// to go: whether it was ours to end.
func endOwnPopup() bool {
	data, err := os.ReadFile(popupFile())
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 || !isOwnPopup(pid) {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil || p.Signal(syscall.SIGTERM) != nil {
		return false
	}
	for range 20 {
		if p.Signal(syscall.Signal(0)) != nil {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return p.Signal(syscall.Signal(0)) != nil
}

// isOwnPopup checks the process is this program, not a stranger that was
// given the pid after ours exited.
func isOwnPopup(pid int) bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	exe, err := processExe(pid)
	return err == nil && sameFile(exe, self)
}

func sameFile(a, b string) bool {
	fa, errA := os.Stat(a)
	fb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}
