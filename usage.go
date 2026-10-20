package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// How much an agent has used, from its session's log: Claude writes each
// reply's tokens there, Codex its running totals and, with them, its
// account's limits. A log is read from where the last read stopped, so a
// long session isn't read again every few seconds.

// tally is what a session's log adds up to so far.
type tally struct {
	path   string
	offset int64
	seen   map[string]bool // Claude's replies, by id: one is logged a line a block
	Model  string
	Last   time.Time // its last reply
	// Tokens: fresh input, output, and what was written to and read from
	// the prompt cache. Reads are the cheap part, and most of it.
	In, Out, CacheWrite, CacheRead int64
	// perMinute is the tokens that count (all but cache reads) by the
	// minute they were used in, for the last hour: how fast it's going.
	perMinute map[int64]int64
	// Codex's account, as of its last reply.
	Codex *codexLimits
}

func newTally(path string) *tally {
	return &tally{path: path, seen: map[string]bool{}, perMinute: map[int64]int64{}}
}

// work is the tokens that count against a limit, more or less: all but
// cache reads, which cost a tenth as much.
func (t *tally) work() int64 { return t.In + t.Out + t.CacheWrite }

// rate is the tokens a minute over the last n minutes to now.
func (t *tally) rate(now time.Time, n int) float64 {
	var sum int64
	m := now.Unix() / 60
	for i := int64(0); i < int64(n); i++ {
		sum += t.perMinute[m-i]
	}
	return float64(sum) / float64(n)
}

// minutes is the tokens of each of the last n minutes, oldest first: a
// graph of how fast it's going.
func (t *tally) minutes(now time.Time, n int) []float64 {
	out := make([]float64, n)
	m := now.Unix() / 60
	for i := range n {
		out[i] = float64(t.perMinute[m-int64(n-1-i)])
	}
	return out
}

// update reads what's been written since the last read. Only whole lines
// are taken: a line being written is left for next time.
func (t *tally) update() {
	f, err := os.Open(t.path)
	if err != nil {
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return
	}
	if fi.Size() < t.offset { // rewritten from the start
		*t = *newTally(t.path)
	}
	if fi.Size() == t.offset {
		return
	}
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return
	}
	r := bufio.NewReaderSize(f, 256*1024)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break // the rest isn't a whole line yet
		}
		t.offset += int64(len(line))
		t.take(line)
	}
	// A day of minutes: the longest the graphs go back.
	cutoff := time.Now().Unix()/60 - minutesKept
	for m := range t.perMinute {
		if m < cutoff {
			delete(t.perMinute, m)
		}
	}
}

// claudeLine is what's read of a Claude log's line.
type claudeLine struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			In         int64 `json:"input_tokens"`
			Out        int64 `json:"output_tokens"`
			CacheWrite int64 `json:"cache_creation_input_tokens"`
			CacheRead  int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// codexLine is what's read of a Codex log's line.
type codexLine struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Payload   struct {
		Type  string `json:"type"`
		Model string `json:"model"`
		Info  *struct {
			Total struct {
				In     int64 `json:"input_tokens"`
				Cached int64 `json:"cached_input_tokens"`
				Out    int64 `json:"output_tokens"`
			} `json:"total_token_usage"`
			Last struct {
				In     int64 `json:"input_tokens"`
				Cached int64 `json:"cached_input_tokens"`
				Out    int64 `json:"output_tokens"`
			} `json:"last_token_usage"`
		} `json:"info"`
		// Decoded apart, so a field it doesn't expect loses the limits, not
		// the line.
		RateLimits json.RawMessage `json:"rate_limits"`
	} `json:"payload"`
}

// flexFloat is a number Codex may send as a string ("72929.10").
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	*f = flexFloat(v)
	return err
}

type codexWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}

type codexRateLimits struct {
	Primary   *codexWindow `json:"primary"`
	Secondary *codexWindow `json:"secondary"`
	Credits   *struct {
		HasCredits bool      `json:"has_credits"`
		Unlimited  bool      `json:"unlimited"`
		Balance    flexFloat `json:"balance"`
	} `json:"credits"`
	PlanType string `json:"plan_type"`
}

// codexLimits is a Codex account's limits as its last reply had them.
type codexLimits struct {
	At     time.Time
	Limits codexRateLimits
}

func (t *tally) take(line []byte) {
	switch {
	case strings.Contains(string(line[:min(len(line), 200)]), `"type":"event_msg"`) ||
		strings.Contains(string(line[:min(len(line), 200)]), `"type":"turn_context"`):
		t.takeCodex(line)
	case strings.Contains(string(line), `"usage"`):
		t.takeClaude(line)
	}
}

func (t *tally) takeClaude(line []byte) {
	var l claudeLine
	if json.Unmarshal(line, &l) != nil || l.Type != "assistant" || l.Message.Usage == nil {
		return
	}
	// A reply is logged once a content block, each line with its whole
	// usage: counted once.
	if id := l.Message.ID; id != "" {
		if t.seen[id] {
			return
		}
		t.seen[id] = true
	}
	u := l.Message.Usage
	t.In += u.In
	t.Out += u.Out
	t.CacheWrite += u.CacheWrite
	t.CacheRead += u.CacheRead
	if l.Message.Model != "" && !strings.HasPrefix(l.Message.Model, "<") { // "<synthetic>": not a model
		t.Model = l.Message.Model
	}
	if !l.Timestamp.IsZero() {
		t.Last = l.Timestamp
		t.perMinute[l.Timestamp.Unix()/60] += u.In + u.Out + u.CacheWrite
	}
}

func (t *tally) takeCodex(line []byte) {
	var l codexLine
	if json.Unmarshal(line, &l) != nil {
		return
	}
	switch {
	case l.Type == "turn_context": // each turn says its model
		if l.Payload.Model != "" {
			t.Model = l.Payload.Model
		}
	case l.Payload.Type == "token_count":
		if i := l.Payload.Info; i != nil {
			// Running totals: input includes what was read from the cache.
			t.In, t.CacheRead, t.Out = i.Total.In-i.Total.Cached, i.Total.Cached, i.Total.Out
			if !l.Timestamp.IsZero() {
				t.Last = l.Timestamp
				t.perMinute[l.Timestamp.Unix()/60] += i.Last.In - i.Last.Cached + i.Last.Out
			}
		}
		var rl codexRateLimits
		if len(l.Payload.RateLimits) > 0 && json.Unmarshal(l.Payload.RateLimits, &rl) == nil && (rl.Primary != nil || rl.Secondary != nil) {
			t.Codex = &codexLimits{At: l.Timestamp, Limits: rl}
		}
	}
}

// modelText is a model, as a person says it: "Opus 5.5", "gpt-5.4".
func modelText(m string) string {
	if !strings.HasPrefix(m, "claude-") {
		return m
	}
	// claude-opus-5-5, claude-sonnet-5-5-20261001: family and version.
	parts := strings.Split(strings.TrimPrefix(m, "claude-"), "-")
	if len(parts) == 0 || parts[0] == "" {
		return m
	}
	name := strings.ToUpper(parts[0][:1]) + parts[0][1:]
	var ver []string
	for _, p := range parts[1:] {
		if len(p) > 2 || p == "" { // a date, or something else
			break
		}
		ver = append(ver, p)
	}
	if len(ver) > 0 {
		name += " " + strings.Join(ver, ".")
	}
	return name
}
