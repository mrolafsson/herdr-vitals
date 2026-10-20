package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A disk fills up over days, not the minutes a graph shows, so how full each
// filesystem is gets written down every few minutes while the monitor is
// open, a file a machine, kept for a fortnight. From that comes when it
// will be full, at the rate it has been filling: the warning that comes in
// time to do something.

const (
	diskLogEvery = 5 * time.Minute
	diskLogKeep  = 14 * 24 * time.Hour
	// The trend is taken over the last three days, and only once there's
	// two hours of it: less is a download, not a trend.
	diskTrendOver = 3 * 24 * time.Hour
	diskTrendMin  = 2 * time.Hour
	// Further off than a month isn't worth a mention.
	diskTrendHorizon = 30 * 24 * time.Hour
)

type diskPoint struct {
	at    time.Time
	mount string
	used  float64
	avail float64
}

// diskLog is one machine's record.
type diskLog struct {
	path    string
	points  []diskPoint
	written time.Time
}

// openDiskLog reads a machine's record, if it has one.
func openDiskLog(t target) *diskLog {
	l := &diskLog{path: filepath.Join(stateDir(), "disks", hostKey(t)+".log")}
	f, err := os.Open(l.path)
	if err != nil {
		return l
	}
	defer f.Close()
	cutoff := time.Now().Add(-diskLogKeep)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var unix int64
		var used, avail float64
		var mount string
		// The mount point is last: it may have spaces in it.
		n, _ := fmt.Sscanf(sc.Text(), "%d %f %f", &unix, &used, &avail)
		if n != 3 {
			continue
		}
		if f := strings.SplitN(sc.Text(), " ", 4); len(f) == 4 {
			mount = f[3]
		}
		at := time.Unix(unix, 0)
		if at.After(cutoff) && mount != "" {
			l.points = append(l.points, diskPoint{at, mount, used, avail})
			l.written = at
		}
	}
	return l
}

// note writes down how full the filesystems are, if it's been a while.
func (l *diskLog) note(s *sample) {
	if s.At.Sub(l.written) < diskLogEvery || len(s.Disks) == 0 {
		return
	}
	l.written = s.At
	var b strings.Builder
	for _, d := range s.Disks {
		l.points = append(l.points, diskPoint{s.At, d.Mount, float64(d.Used), float64(d.Avail)})
		fmt.Fprintf(&b, "%d %d %d %s\n", s.At.Unix(), d.Used, d.Avail, d.Mount)
	}
	// Now and then, the file without what's too old to keep.
	cutoff := s.At.Add(-diskLogKeep)
	if len(l.points) > 0 && l.points[0].at.Before(cutoff.Add(-24*time.Hour)) {
		kept := l.points[:0]
		b.Reset()
		for _, p := range l.points {
			if p.at.After(cutoff) {
				kept = append(kept, p)
				fmt.Fprintf(&b, "%d %d %d %s\n", p.at.Unix(), uint64(p.used), uint64(p.avail), p.mount)
			}
		}
		l.points = kept
		if os.MkdirAll(filepath.Dir(l.path), 0o700) == nil {
			_ = os.WriteFile(l.path, []byte(b.String()), 0o600)
		}
		return
	}
	if os.MkdirAll(filepath.Dir(l.path), 0o700) != nil {
		return
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(b.String())
}

// fills is how long each filesystem has until it's full, at the rate it's
// been filling, for those that will be within a month.
func (l *diskLog) fills(now time.Time) map[string]time.Duration {
	byMount := map[string][]diskPoint{}
	for _, p := range l.points {
		if now.Sub(p.at) <= diskTrendOver {
			byMount[p.mount] = append(byMount[p.mount], p)
		}
	}
	out := map[string]time.Duration{}
	for mount, ps := range byMount {
		if d, ok := timeToFull(ps); ok {
			out[mount] = d
		}
	}
	return out
}

// timeToFull fits a straight line to how much is used over time (least
// squares) and says when what's left would run out at its slope.
func timeToFull(ps []diskPoint) (time.Duration, bool) {
	if len(ps) < 4 || ps[len(ps)-1].at.Sub(ps[0].at) < diskTrendMin {
		return 0, false
	}
	t0 := ps[0].at
	var n, sx, sy, sxx, sxy float64
	for _, p := range ps {
		x := p.at.Sub(t0).Seconds()
		n, sx, sy, sxx, sxy = n+1, sx+x, sy+p.used, sxx+x*x, sxy+x*p.used
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0, false
	}
	slope := (n*sxy - sx*sy) / den // bytes a second
	if slope <= 0 {
		return 0, false
	}
	left := time.Duration(ps[len(ps)-1].avail / slope * float64(time.Second))
	if left > diskTrendHorizon {
		return 0, false
	}
	return left, true
}

// fillLevel is how soon full calls for you: within a day now, within a
// week a look.
func fillLevel(d time.Duration) level {
	switch {
	case d < 24*time.Hour:
		return levelHot
	case d < 7*24*time.Hour:
		return levelWarn
	}
	return levelOK
}

// fillText is how long until full, briefly: "~3d", "~5h", "~40m".
func fillText(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return "~" + itoa(int(d.Hours()/24+0.5)) + "d"
	case d >= time.Hour:
		return "~" + itoa(int(d.Hours()+0.5)) + "h"
	}
	return "~" + itoa(max(1, int(d.Minutes()))) + "m"
}
