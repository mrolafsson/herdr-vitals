package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// How full each account's limits have been, over the last day: Anthropic
// says only how full they are now, so the sampler writes it down each time
// it asks (every few minutes), and the Agents screen draws it under the
// account's usage, on the same clock, to show the one driving the other.

// limitPoint is an account's limits at a moment: how full each, by label.
type limitPoint struct {
	T int64              `json:"t"`
	P map[string]float64 `json:"p"`
}

func limitLogFile() string { return filepath.Join(stateDir(), "limits.json") }

func readLimitLog() map[string][]limitPoint {
	out := map[string][]limitPoint{}
	if data, err := os.ReadFile(limitLogFile()); err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}

// noteLimits writes down how full each account's limits are now, if it's
// been a few minutes since they were last written, and forgets what's
// over a day old.
func noteLimits(accounts []account, now time.Time) {
	log := readLimitLog()
	changed := false
	for _, a := range accounts {
		if len(a.Limits) == 0 || a.At.IsZero() {
			continue
		}
		ps := log[a.Key]
		// The figures as of when they were said, not when they were read.
		at := a.At.Unix()
		if len(ps) > 0 && at-ps[len(ps)-1].T < 4*60 {
			continue
		}
		p := limitPoint{T: at, P: map[string]float64{}}
		for _, l := range a.Limits {
			p.P[l.Label] = l.Percent
		}
		log[a.Key] = append(ps, p)
		changed = true
	}
	cutoff := now.Add(-24 * time.Hour).Unix()
	for k, ps := range log {
		i := 0
		for i < len(ps) && ps[i].T < cutoff {
			i++
		}
		if i > 0 {
			log[k], changed = ps[i:], true
		}
		if len(log[k]) == 0 {
			delete(log, k)
		}
	}
	if !changed || os.MkdirAll(stateDir(), 0o700) != nil {
		return
	}
	if data, err := json.Marshal(log); err == nil {
		tmp := limitLogFile() + ".tmp"
		if os.WriteFile(tmp, data, 0o600) == nil {
			_ = os.Rename(tmp, limitLogFile())
		}
	}
}

// limitColumns is how full a limit was across width columns spanning the
// last span to now: each column the figure last said by its end, or -1
// where nothing had been (or it's over a quarter-hour old: a gap).
func limitColumns(ps []limitPoint, label string, width int, span time.Duration, now time.Time) []float64 {
	out := make([]float64, width)
	from := now.Add(-span)
	j := -1
	for c := range out {
		end := from.Add(span * time.Duration(c+1) / time.Duration(width)).Unix()
		for j+1 < len(ps) && ps[j+1].T <= end {
			j++
		}
		out[c] = -1
		if j >= 0 && end-ps[j].T <= 15*60 {
			if v, ok := ps[j].P[label]; ok {
				out[c] = v
			}
		}
	}
	return out
}

// usageColumns is tokens a minute, a day of minutes (oldest first, ending
// now) spread over width columns spanning the last span: each column the
// mean of its minutes, each minute the mean of the five to it. A reply's
// tokens all land in its minute, so minute by minute it's spikes; over
// five, it's how hard the agents are working.
func usageColumns(minutes []float64, width int, span time.Duration) []float64 {
	out := make([]float64, width)
	n := int(span.Minutes())
	if len(minutes) == 0 || n <= 0 {
		return out
	}
	minutes = smooth(minutes, 5)
	if n > len(minutes) {
		n = len(minutes)
	}
	recent := minutes[len(minutes)-n:]
	for c := range out {
		lo, hi := c*n/width, (c+1)*n/width
		if hi <= lo {
			hi = lo + 1
		}
		sum := 0.0
		for _, v := range recent[lo:min(hi, n)] {
			sum += v
		}
		out[c] = sum / float64(hi-lo)
	}
	return out
}

// smooth is each value the mean of the k to it.
func smooth(vs []float64, k int) []float64 {
	out := make([]float64, len(vs))
	sum := 0.0
	for i, v := range vs {
		sum += v
		if i >= k {
			sum -= vs[i-k]
		}
		out[i] = sum / float64(min(i+1, k))
	}
	return out
}

// usageTop is the value that fills a usage graph: a little over its busy
// moments (the ninetieth percentile of them), not its busiest, so a rare
// burst (a session resumed, its whole context written to the cache at
// once) runs off the top rather than flattening everything else. clipped:
// something does.
func usageTop(vs []float64) (top float64, clipped bool) {
	var busy []float64
	for _, v := range vs {
		if v > 0 {
			busy = append(busy, v)
		}
	}
	if len(busy) == 0 {
		return 1000, false
	}
	sort.Float64s(busy)
	top = max(1000, busy[len(busy)*9/10]*1.25)
	if peak := busy[len(busy)-1]; peak <= top {
		return max(1000, peak), false
	}
	return top, true
}

// sumMinutes adds agents' minutes together: an account's usage.
func sumMinutes(rows []agentRow) []float64 {
	out := make([]float64, minutesKept)
	for _, r := range rows {
		off := minutesKept - len(r.Minutes)
		for i, v := range r.Minutes {
			if off+i >= 0 {
				out[off+i] += v
			}
		}
	}
	return out
}
