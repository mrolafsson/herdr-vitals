package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// The agents and their accounts, put together: what the Agents screen
// shows and the sampler toasts about.

// agentRow is an agent, what it's used and whose account it's on.
type agentRow struct {
	Info    agentInfo
	Space   string // its workspace's name
	Model   string
	Work    int64 // tokens that count: all but cache reads
	Cached  int64 // tokens read from the cache
	Rate    float64
	Minutes []float64 // tokens a minute, the last day, oldest first
	Last    time.Time
	Account string // its account's key, "" if it's not known
}

// aiUsage is a moment's view of every agent and account.
type aiUsage struct {
	At       time.Time
	Agents   []agentRow
	Accounts []account
	History  map[string][]limitPoint // how full each account's limits were, by key
}

// minutesKept is how far back the usage graphs can go: a day.
const minutesKept = 24 * 60

// collector keeps what's needed between looks: where each agent's log is,
// and what's been read of it.
type collector struct {
	runs    map[string]foundRun // by pane
	tallies map[string]*tally   // by log
	clauth  map[string]clauthProfile
	clAt    time.Time
}

type foundRun struct {
	run agentRun
	at  time.Time
}

func newCollector() *collector {
	return &collector{runs: map[string]foundRun{}, tallies: map[string]*tally{}}
}

// tallyOf is a log's tally, brought up to date.
func (c *collector) tallyOf(path string) *tally {
	t := c.tallies[path]
	if t == nil {
		t = newTally(path)
		c.tallies[path] = t
	}
	t.update()
	return t
}

// collect looks at every agent and account now. It may ask Anthropic for a
// login's limits, when they're due: run it off the UI's thread.
func (c *collector) collect(ctx context.Context, now time.Time) aiUsage {
	agents, _ := listAgents()
	spaces := workspaceNames()
	u := aiUsage{At: now}
	var logins []string
	codexHomes := map[string]*codexLimits{}
	for _, a := range agents {
		// Where an agent's log is doesn't change, but a new agent's log only
		// appears once something's been said: until then it's looked for again.
		f, ok := c.runs[a.PaneID]
		if !ok || (f.run.Transcript == "" && now.Sub(f.at) > 10*time.Second) {
			f = foundRun{run: findRun(a), at: now}
			c.runs[a.PaneID] = f
		}
		row := agentRow{Info: a, Space: spaces[a.WorkspaceID]}
		r := f.run
		if r.Transcript != "" {
			t := c.tallyOf(r.Transcript)
			row.Model, row.Work, row.Cached, row.Last = t.Model, t.work(), t.CacheRead, t.Last
			row.Rate, row.Minutes = t.rate(now, 5), t.minutes(now, minutesKept)
			if a.Agent == "codex" && t.Codex != nil {
				if have := codexHomes[r.ConfigDir]; have == nil || t.Codex.At.After(have.At) {
					codexHomes[r.ConfigDir] = t.Codex
				}
			}
		}
		switch a.Agent {
		case "claude":
			logins = append(logins, r.Login)
			row.Account = r.Login // made the account's key below
		case "codex":
			if r.ConfigDir != "" {
				row.Account = "codex:" + r.ConfigDir
				if _, ok := codexHomes[r.ConfigDir]; !ok {
					codexHomes[r.ConfigDir] = nil
				}
			}
		}
		u.Agents = append(u.Agents, row)
	}
	// Panes gone are forgotten, and so are their logs.
	live := map[string]bool{}
	for _, a := range agents {
		live[a.PaneID] = true
	}
	for p, f := range c.runs {
		if !live[p] {
			delete(c.runs, p)
			delete(c.tallies, f.run.Transcript)
		}
	}

	// Codex: ~/.codex too, with no agent running there, from its newest log.
	home, _ := os.UserHomeDir()
	if def := filepath.Join(home, ".codex"); codexHomes[def] == nil {
		if f := newestCodexSession(def); f != "" {
			if t := c.tallyOf(f); t.Codex != nil {
				codexHomes[def] = t.Codex
			}
		}
	}

	// Claude: every login, refreshed when due, from the shared book.
	if now.Sub(c.clAt) > time.Minute {
		c.clauth, c.clAt = clauthProfiles(), now
	}
	all := claudeLogins(logins, c.clauth)
	b := loadBook()
	b.refreshClaude(ctx, all, c.clauth, now)
	b.save()
	seen := map[string]bool{}
	for _, l := range all {
		key := b.Keys[l]
		if a, ok := b.Accounts[key]; ok && !seen[key] {
			seen[key] = true
			u.Accounts = append(u.Accounts, a)
		}
	}
	// clauth's names, now: they may be newer than the figures.
	for i, a := range u.Accounts {
		for _, l := range all {
			if b.Keys[l] == a.Key && c.clauth[l].Name != "" {
				u.Accounts[i].Name = c.clauth[l].Name
			}
		}
	}
	for i := range u.Agents {
		if u.Agents[i].Info.Agent == "claude" {
			u.Agents[i].Account = b.Keys[u.Agents[i].Account]
		}
	}
	sort.SliceStable(u.Accounts, func(i, j int) bool { return u.Accounts[i].Name < u.Accounts[j].Name })
	var homes []string
	for h := range codexHomes {
		homes = append(homes, h)
	}
	sort.Strings(homes)
	for _, h := range homes {
		u.Accounts = append(u.Accounts, codexAccount(h, codexHomes[h]))
	}
	// Whoever looks writes down how full the limits are: the sampler, or
	// the screen itself.
	noteLimits(u.Accounts, now)
	u.History = readLimitLog()
	return u
}

// debugAgents prints what the Agents screen would show, as text.
func debugAgents(ctx context.Context) error {
	now := time.Now()
	u := newCollector().collect(ctx, now)
	for _, a := range u.Accounts {
		fmt.Printf("%s  %s %s  as of %s %s\n", a.Name, a.Kind, a.Plan, agoText(a.At, now), a.Err)
		for _, l := range a.Limits {
			fmt.Printf("    %-8s %5.1f%%  %-8s resets %s\n", l.Label, l.Percent, l.Severity, resetText(l.ResetsAt, now))
		}
		if a.Credits != "" {
			fmt.Println("    " + a.Credits)
		}
		for _, r := range u.Agents {
			if r.Account == a.Key {
				fmt.Printf("    %-8s %-40.40s %-10s %8s work %8s cached %6.0f/min\n", r.Info.Status, r.Info.title(), modelText(r.Model),
					countText(int(r.Work)), countText(int(r.Cached)), r.Rate)
			}
		}
	}
	for _, r := range u.Agents {
		if r.Account == "" {
			fmt.Printf("(no account) %s %s %s\n", r.Info.Agent, r.Info.PaneID, r.Info.title())
		}
	}
	return nil
}
