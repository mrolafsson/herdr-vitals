package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// The coding agents herdr knows, and for each, where its conversation is
// written down (its usage is read from there: usage.go) and which login it
// runs under (its account's limits come from there: accounts.go). Found as
// herdr-recap finds them: from the agent's process, which says which
// config folder it uses, and so which login and which session.

// agentInfo is one entry of herdr's agent.list.
type agentInfo struct {
	PaneID       string            `json:"pane_id"`
	WorkspaceID  string            `json:"workspace_id"`
	Agent        string            `json:"agent"` // claude, codex, …
	Name         string            `json:"name"`
	Status       string            `json:"agent_status"`
	Cwd          string            `json:"cwd"`
	Title        string            `json:"terminal_title_stripped"`
	Tokens       map[string]string `json:"tokens"`
	AgentSession struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	} `json:"agent_session"`
}

// title is what an agent is called: its terminal's title (Claude's title
// for the conversation), else its name, else its folder.
func (a agentInfo) title() string {
	for _, t := range []string{a.Title, a.Name, filepath.Base(a.Cwd)} {
		if t = strings.TrimSpace(t); t != "" && t != "." && t != "/" && !strings.EqualFold(t, "claude code") {
			return t
		}
	}
	return a.Agent
}

func listAgents() ([]agentInfo, error) {
	var res struct {
		Agents []agentInfo `json:"agents"`
	}
	err := herdrCall("agent.list", nil, &res)
	sanitize(&res.Agents)
	return res.Agents, err
}

// workspaceNames is each workspace's name, for saying where an agent is.
func workspaceNames() map[string]string {
	var res struct {
		Workspaces []struct {
			ID    string `json:"workspace_id"`
			Label string `json:"label"`
		} `json:"workspaces"`
	}
	out := map[string]string{}
	if herdrCall("workspace.list", nil, &res) == nil {
		sanitize(&res.Workspaces)
		for _, w := range res.Workspaces {
			out[w.ID] = w.Label
		}
	}
	return out
}

// process is one of a pane's foreground processes.
type process struct {
	PID  int      `json:"pid"`
	Name string   `json:"name"`
	Argv []string `json:"argv"`
}

func paneProcesses(paneID string) ([]process, error) {
	var res struct {
		ProcessInfo struct {
			Foreground []process `json:"foreground_processes"`
		} `json:"process_info"`
	}
	err := herdrCall("pane.process_info", map[string]any{"pane_id": paneID}, &res)
	return res.ProcessInfo.Foreground, err
}

// agentRun is where an agent's conversation and login are.
type agentRun struct {
	Transcript string // its session's log; "" until something's been said
	ConfigDir  string // CLAUDE_CONFIG_DIR or CODEX_HOME, or the default
	Login      string // Claude's credentials file, followed to where it really is
}

var (
	sessionIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$`)
	notAlnum         = regexp.MustCompile(`[^a-zA-Z0-9]`)
)

// findRun finds where an agent's conversation and login are, for Claude and
// Codex; nothing for others.
func findRun(a agentInfo) agentRun {
	procs, err := paneProcesses(a.PaneID)
	if err != nil || len(procs) == 0 {
		return agentRun{}
	}
	switch a.Agent {
	case "claude":
		return claudeRun(procs)
	case "codex":
		return codexRun(a, procs)
	}
	return agentRun{}
}

// claudeRun is a Claude agent's: its config folder from its process (a
// profile switcher, clauth say, gives each profile its own), its session
// from Claude's file for that process.
func claudeRun(procs []process) agentRun {
	pid := 0
	for _, p := range procs { // the last: a wrapper comes before what it runs
		name := p.Name
		if len(p.Argv) > 0 {
			name = filepath.Base(p.Argv[0])
		}
		if name == "claude" || p.Name == "claude" {
			pid = p.PID
		}
	}
	if pid == 0 {
		return agentRun{}
	}
	env, _ := readProcessEnv(pid)
	r := agentRun{ConfigDir: env["CLAUDE_CONFIG_DIR"]}
	if r.ConfigDir == "" {
		home, _ := os.UserHomeDir()
		r.ConfigDir = filepath.Join(home, ".claude")
	}
	r.Login = loginFile(r.ConfigDir)
	data, err := os.ReadFile(filepath.Join(r.ConfigDir, "sessions", strconv.Itoa(pid)+".json"))
	if err != nil {
		return r
	}
	var f struct {
		SessionID string `json:"sessionId"`
		Cwd       string `json:"cwd"`
	}
	if json.Unmarshal(data, &f) != nil || !sessionIDPattern.MatchString(f.SessionID) {
		return r
	}
	r.Transcript = claudeTranscript(r.ConfigDir, f.Cwd, f.SessionID)
	return r
}

// loginFile is a Claude config folder's credentials, followed through any
// links (clauth's profiles link theirs), so two folders on one login are
// seen as one. "" if there are none (macOS keeps them in the Keychain).
func loginFile(configDir string) string {
	p := filepath.Join(configDir, ".credentials.json")
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return ""
}

// claudeTranscript is where Claude keeps a session's conversation:
// projects/<cwd, every other character a dash>/<id>.jsonl, or the same name
// under another project, or under the default folder (clauth's profiles
// share it).
func claudeTranscript(configDir, cwd, id string) string {
	home, _ := os.UserHomeDir()
	for _, dir := range []string{configDir, filepath.Join(home, ".claude")} {
		projects := filepath.Join(dir, "projects")
		p := filepath.Join(projects, notAlnum.ReplaceAllString(cwd, "-"), id+".jsonl")
		if _, err := os.Stat(p); err == nil {
			return p
		}
		if m, _ := filepath.Glob(filepath.Join(projects, "*", id+".jsonl")); len(m) > 0 {
			return m[0]
		}
	}
	return ""
}

// codexRun is a Codex agent's: the session file its process has open, or
// the one herdr's session ID names.
func codexRun(a agentInfo, procs []process) agentRun {
	var r agentRun
	pid := procs[len(procs)-1].PID
	for i := len(procs) - 1; i >= 0 && r.Transcript == ""; i-- {
		for _, f := range openFiles(procs[i].PID) {
			if codexSessionID(f) != "" {
				r.Transcript, pid = f, procs[i].PID
				break
			}
		}
	}
	env, _ := readProcessEnv(pid)
	r.ConfigDir = env["CODEX_HOME"]
	if r.ConfigDir == "" {
		home, _ := os.UserHomeDir()
		r.ConfigDir = filepath.Join(home, ".codex")
	}
	if id := a.AgentSession.Value; r.Transcript == "" && a.AgentSession.Kind == "id" && sessionIDPattern.MatchString(id) {
		if m, _ := filepath.Glob(filepath.Join(r.ConfigDir, "sessions", "*", "*", "*", "rollout-*-"+id+".jsonl")); len(m) > 0 {
			r.Transcript = m[0]
		}
	}
	return r
}

// codexSessionID is the session a Codex session file is for, from its name:
// rollout-<when>-<id>.jsonl under sessions/. "" for any other file.
func codexSessionID(path string) string {
	name := filepath.Base(path)
	if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") ||
		!strings.Contains(filepath.ToSlash(path), "/sessions/") {
		return ""
	}
	name = strings.TrimSuffix(name, ".jsonl")
	if len(name) < 36 || !sessionIDPattern.MatchString(name[len(name)-36:]) {
		return ""
	}
	return name[len(name)-36:]
}

// newestCodexSession is the Codex session last written to under a Codex
// folder, for an account with no agent running: its last reply has its
// limits. The newest day's folder is enough to look in.
func newestCodexSession(codexHome string) string {
	days, _ := filepath.Glob(filepath.Join(codexHome, "sessions", "*", "*", "*"))
	if len(days) == 0 {
		return ""
	}
	day := days[len(days)-1] // year/month/day sort as they read
	files, _ := filepath.Glob(filepath.Join(day, "rollout-*.jsonl"))
	newest, at := "", int64(0)
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil && fi.ModTime().UnixNano() > at {
			newest, at = f, fi.ModTime().UnixNano()
		}
	}
	return newest
}
