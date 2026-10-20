package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// A toast when a machine runs low: CPU flat out for a couple of minutes,
// memory nearly gone, a disk nearly full or about to be. Each says so once,
// and not again until it has eased off (well below the line, so hovering at
// it doesn't toast on and off), however long that takes. What's been said is
// kept, so a restart of herdr doesn't say it all again.

const (
	cpuAlertOver   = 2 * time.Minute // CPU at 90% for this long, not a spike
	fillAlertUnder = 24 * time.Hour  // a disk this close to full
)

// alerts is what's been said and not yet eased off: "atlas cpu",
// "atlas disk /data", by machine and what.
type alerts struct {
	active map[string]bool
	notify func(title, body string) // herdr's toast; tests stand one in
}

func alertsFile() string { return filepath.Join(stateDir(), "alerts.json") }

func loadAlerts() *alerts {
	a := &alerts{active: map[string]bool{}, notify: notify}
	if data, err := os.ReadFile(alertsFile()); err == nil {
		_ = json.Unmarshal(data, &a.active)
	}
	return a
}

func (a *alerts) save() {
	if data, err := json.Marshal(a.active); err == nil {
		_ = os.WriteFile(alertsFile(), data, 0o600)
	}
}

// raise toasts what if it hasn't been said; ease forgets it once it's well
// clear, so it can be said again.
func (a *alerts) raise(key, body string) {
	if a.active[key] {
		return
	}
	a.active[key] = true
	a.notify("Vitals", body)
	a.save()
}

func (a *alerts) ease(key string) {
	if a.active[key] {
		delete(a.active, key)
		a.save()
	}
}

// checkAccounts looks at the accounts' limits for anything to say: at 80%
// of one, and again at 95%, each until it's back under 70% (a reset, say).
func (a *alerts) checkAccounts(accounts []account, now time.Time) {
	for _, acct := range accounts {
		for _, l := range shownLimits(acct.Limits) {
			key := "limit " + acct.Key + " " + l.Label
			if l.Percent < 70 {
				a.ease(key + " 80")
				a.ease(key + " 95")
				continue
			}
			what := l.Label + " limit"
			if l.Label == "week" || l.Label == "session" {
				what = map[string]string{"week": "weekly limit", "session": "session limit"}[l.Label]
			}
			body := acct.Name + ": " + what + " at " + pctText(l.Percent)
			if r := resetText(l.ResetsAt, now); r != "" {
				body += ", resets " + r
			}
			switch {
			case l.Percent >= 95:
				// Past both at once: said once, for 95, and 80 counted as said.
				if !a.active[key+" 80"] {
					a.active[key+" 80"] = true
				}
				a.raise(key+" 95", body)
			case l.Percent >= 80:
				a.raise(key+" 80", body)
			}
		}
	}
}

// check looks at a machine's round for anything to say.
func (a *alerts) check(t target, s *sample, v vitals, recent []point, fills map[string]time.Duration) {
	name := t.Name
	// CPU: flat out over the last couple of minutes, every round of them.
	since := s.At.Add(-cpuAlertOver).Unix()
	var n, sum float64
	covered := false
	for _, p := range recent {
		if p.T >= since {
			n, sum = n+1, sum+p.CPU
		}
		covered = covered || p.T <= since+5
	}
	if covered && n > 0 {
		switch avg := sum / n; {
		case avg >= hotPct:
			a.raise(name+" cpu", name+": CPU at "+pctText(avg)+" for the last two minutes")
		case avg < warnPct:
			a.ease(name + " cpu")
		}
	}
	switch {
	case v.Mem >= hotPct:
		a.raise(name+" memory", name+": memory at "+pctText(v.Mem)+", "+bytesText(s.MemAvail)+" left")
	case v.Mem < warnPct:
		a.ease(name + " memory")
	}
	for _, d := range s.Disks {
		key := name + " disk " + d.Mount
		switch p := diskPct(d); {
		case p >= hotPct:
			a.raise(key, name+": "+d.Mount+" is "+pctText(p)+" full, "+bytesText(d.Avail)+" left")
		case p < warnPct:
			a.ease(key)
		}
		key = name + " fills " + d.Mount
		switch left, ok := fills[d.Mount]; {
		case ok && left < fillAlertUnder:
			a.raise(key, name+": "+d.Mount+" will be full in "+fillText(left)+", at the rate it's filling")
		case !ok || left > 2*fillAlertUnder:
			a.ease(key)
		}
	}
}
