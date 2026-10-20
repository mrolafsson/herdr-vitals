package main

import (
	"strconv"
	"strings"
)

func itoa(n int) string { return strconv.Itoa(n) }

// bytesText is a size, briefly, in powers of 1024: "512B", "4.1K", "19.2G",
// "1.5T". Under 10 of a unit it keeps a decimal.
func bytesText(n uint64) string {
	v, units := float64(n), "BKMGTP"
	i := 0
	for v >= 1000 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return strconv.FormatUint(n, 10) + "B"
	}
	if v < 10 {
		return strconv.FormatFloat(v, 'f', 1, 64) + units[i:i+1]
	}
	return strconv.FormatFloat(v, 'f', 0, 64) + units[i:i+1]
}

// countText is a count, briefly: "418", "2.1k", "12k".
func countText(n int) string {
	switch {
	case n >= 10000:
		return strconv.Itoa(n/1000) + "k"
	case n >= 1000:
		return strconv.FormatFloat(float64(n)/1000, 'f', 1, 64) + "k"
	}
	return strconv.Itoa(n)
}

// pctText is a percentage as a whole number: "62%".
func pctText(p float64) string {
	return strconv.Itoa(int(p+0.5)) + "%"
}

// releaseText is what a machine runs, briefly: "Ubuntu 24.04", not "Ubuntu
// 24.04.1 LTS"; the kernel if it says nothing else.
func releaseText(s *sample) string {
	r := strings.TrimSuffix(strings.TrimSpace(s.Release), " LTS")
	// "Debian GNU/Linux 12 (bookworm)" is Debian 12.
	if i := strings.Index(r, " ("); i > 0 {
		r = r[:i]
	}
	r = strings.Replace(r, " GNU/Linux", "", 1)
	if r == "" || r == "macOS" {
		r = s.Kernel
	}
	// A point release is more than a glance needs.
	if f := strings.Fields(r); len(f) == 2 {
		if parts := strings.Split(f[1], "."); len(parts) > 2 {
			r = f[0] + " " + parts[0] + "." + parts[1]
		}
	}
	return r
}

// shorten cuts s to n cells, with an ellipsis if it had to (look.go's name
// for fit).
func shorten(s string, n int) string { return fit(s, n) }
