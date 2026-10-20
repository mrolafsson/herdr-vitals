package main

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"time"
)

// reading is a line of `stream`: this machine as it is now. Other plugins
// read it (herdr-recap draws it in its header), so its names stay put.
type reading struct {
	CPU   float64 `json:"cpu"`  // 0–100, every core together
	Mem   float64 `json:"mem"`  // % in use
	Load  float64 `json:"load"` // the 1-minute load a core: 1 is every core busy
	Cores int     `json:"cores"`
}

// streamEvery is how often stream reads: a second, so a small graph moves.
const streamEvery = time.Second

// stream writes a reading of this machine a second, until ctx ends or no one
// reads them. The first comes once there are two rounds to tell CPU from.
func stream(ctx context.Context, w io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := make(chan frameMsg, 4)
	go watch(ctx, 0, target{Name: "this", Origin: "this"}, streamEvery, true, out)
	enc := json.NewEncoder(w)
	// The last couple of minutes the sampler kept, first, so a graph drawn from
	// this starts full.
	if ps := freshRecent(target{Origin: "this"}, streamEvery*2, time.Now()); len(ps) > 0 {
		for _, p := range ps[max(0, len(ps)-64):] {
			r := reading{CPU: round1(p.CPU), Mem: round1(p.Mem), Load: math.Round(p.Load*100) / 100}
			if enc.Encode(r) != nil {
				return nil
			}
		}
	}
	var prev *sample
	for {
		select {
		case <-ctx.Done():
			return nil
		case f := <-out:
			if f.sample == nil {
				continue
			}
			v := derive(prev, f.sample)
			first := prev == nil && f.sample.OS != "darwin"
			prev = f.sample
			if first {
				continue
			}
			r := reading{CPU: round1(v.CPU), Mem: round1(v.Mem), Load: math.Round(v.LoadPerCore*100) / 100, Cores: f.sample.Cores}
			if err := enc.Encode(r); err != nil {
				return nil // the reader's gone: so are we
			}
		}
	}
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
