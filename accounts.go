package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The accounts the agents run on, and how near each is to its limits.
//
// Claude: each login's limits come from what Claude Code's /usage asks
// (api/oauth/usage), with that login: the one in ~/.claude, each running
// agent's (its CLAUDE_CONFIG_DIR's), and with clauth installed, each of
// its profiles. Asked at most every few minutes, the answer kept in the
// plugin's state folder for whatever shows it next. A login whose token
// has run out is left until Claude Code renews it: renewing it here could
// sign Claude Code out. Two logins on one account (api/oauth/profile says
// which) are one account.
//
// Codex: each Codex folder's limits are in its session logs, as of its
// last reply (usage.go). Nothing to ask.

const (
	limitsEvery  = 5 * time.Minute // a login's limits asked for no more often
	profileEvery = 24 * time.Hour  // who a login is, asked for this often
	clauthFresh  = 5 * time.Minute // clauth's own figures, used if this new

)

// apiBase is Anthropic's API; a variable so tests can stand one in.
var apiBase = "https://api.anthropic.com"

// limitBar is one of an account's limits.
type limitBar struct {
	Label    string    `json:"label"`   // "session", "week", "Fable"
	Percent  float64   `json:"percent"` // used
	Severity string    `json:"severity,omitempty"`
	ResetsAt time.Time `json:"resets_at"`
	Active   bool      `json:"active,omitempty"` // the one that binds now
	Model    bool      `json:"model,omitempty"`  // for one model (Fable)
}

// account is an account's limits, as of At.
type account struct {
	Key     string     `json:"key"`
	Kind    string     `json:"kind"` // claude, codex
	Name    string     `json:"name"`
	Plan    string     `json:"plan,omitempty"`
	Limits  []limitBar `json:"limits"`
	Credits string     `json:"credits,omitempty"`
	At      time.Time  `json:"at"`
	Err     string     `json:"err,omitempty"`
}

// level is how near an account is to its nearest limit.
func (a account) level() level {
	worst := levelOK
	for _, l := range a.Limits {
		worst = max(worst, limitLevel(l))
	}
	return worst
}

// limitLevel is a limit's: Anthropic's own say, when it says more than
// normal, else from 80% a look, from 95% now.
func limitLevel(l limitBar) level {
	switch {
	case l.Percent >= 95 || l.Severity == "critical" || l.Severity == "exceeded":
		return levelHot
	case l.Percent >= 80 || (l.Severity != "" && l.Severity != "normal"):
		return levelWarn
	}
	return levelOK
}

// who is who a Claude login is, from api/oauth/profile.
type who struct {
	Account string    `json:"account"` // its id and its organization's: the account
	Name    string    `json:"name"`    // email, for want of clauth's name
	Plan    string    `json:"plan"`
	At      time.Time `json:"at"`
}

// book is what's kept of the accounts between reads, in the state folder.
type book struct {
	// Version is how what's kept is written: a book in another's is asked
	// for afresh, its figures being in names this one doesn't know.
	Version  int                  `json:"version"`
	Accounts map[string]account   `json:"accounts"` // by key
	Who      map[string]who       `json:"who"`      // by login
	Asked    map[string]time.Time `json:"asked"`    // by login: when its limits were last asked for
	Keys     map[string]string    `json:"keys"`     // login → its account's key
	Retry    map[string]time.Time `json:"retry"`    // by login: not before, after being turned away
}

func bookFile() string { return filepath.Join(stateDir(), "accounts.json") }

// bookVersion is the book's present way: 2 says "session" for the five
// hours, and marks a model's own limits.
const bookVersion = 2

func loadBook() *book {
	b := &book{}
	if data, err := os.ReadFile(bookFile()); err == nil {
		_ = json.Unmarshal(data, b)
	}
	if b.Version != bookVersion {
		*b = book{Version: bookVersion, Who: b.Who}
	}
	if b.Accounts == nil {
		b.Accounts = map[string]account{}
	}
	if b.Who == nil {
		b.Who = map[string]who{}
	}
	if b.Asked == nil {
		b.Asked = map[string]time.Time{}
	}
	if b.Keys == nil {
		b.Keys = map[string]string{}
	}
	if b.Retry == nil {
		b.Retry = map[string]time.Time{}
	}
	return b
}

func (b *book) save() {
	data, err := json.Marshal(b)
	if err != nil || os.MkdirAll(stateDir(), 0o700) != nil {
		return
	}
	tmp := bookFile() + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, bookFile())
	}
}

// claudeLogins is every Claude login to look at: ~/.claude's, the agents'
// (logins), and clauth's profiles, each with clauth's name for it if it
// has one.
func claudeLogins(agentLogins []string, cl map[string]clauthProfile) []string {
	seen := map[string]bool{}
	var out []string
	add := func(l string) {
		if l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	home, _ := os.UserHomeDir()
	add(loginFile(filepath.Join(home, ".claude")))
	if runtime.GOOS == "darwin" && loginFile(filepath.Join(home, ".claude")) == "" {
		add(keychainLogin) // macOS keeps ~/.claude's in the Keychain
	}
	for _, l := range agentLogins {
		add(l)
	}
	for l := range cl {
		add(l)
	}
	sort.Strings(out)
	return out
}

// refreshClaude brings every login's limits up to date that's due, and
// says which account each is.
func (b *book) refreshClaude(ctx context.Context, logins []string, cl map[string]clauthProfile, now time.Time) {
	for _, login := range logins {
		if now.Sub(b.Asked[login]) < limitsEvery || now.Before(b.Retry[login]) {
			continue
		}
		b.Asked[login] = now
		key, acct, wait := b.claudeAccount(ctx, login, cl[login], now)
		if wait > 0 {
			// Asked too often, by everyone on the account: as long as it says
			// (a minute, often), a moment more so as not to land with the rest.
			wait = min(15*time.Minute, wait+5*time.Second)
			b.Retry[login] = now.Add(wait)
			b.Asked[login] = time.Time{} // and then straight away
			acct.Err = "busy · trying again " + now.Add(wait).Local().Format("15:04")
		}
		if key == "" {
			continue
		}
		b.Keys[login] = key
		// A login that couldn't say keeps what was said last, and why not.
		if prev, ok := b.Accounts[key]; ok && acct.Err != "" && len(acct.Limits) == 0 {
			prev.Err = acct.Err
			acct = prev
		}
		b.Accounts[key] = acct
	}
}

// claudeAccount is a login's account and its limits now.
func (b *book) claudeAccount(ctx context.Context, login string, cp clauthProfile, now time.Time) (string, account, time.Duration) {
	tok, err := claudeToken(login, now)
	w, known := b.Who[login]
	if err == nil && (!known || now.Sub(w.At) > profileEvery) {
		if got, err2 := fetchWho(ctx, tok); err2 == nil {
			w, known = got, true
			w.At = now
			b.Who[login] = w
		}
	}
	key := "claude:" + login // until it's known whose it is
	if known {
		key = "claude:" + w.Account
	}
	a := account{Key: key, Kind: "claude", Name: w.Name, Plan: w.Plan, At: now}
	if cp.Name != "" {
		a.Name = cp.Name
		if cp.Plan != "" {
			a.Plan = cp.Plan
		}
	}
	if a.Name == "" {
		a.Name = filepath.Base(filepath.Dir(login))
	}
	// clauth asked a moment ago: its answer, not another question.
	if cp.Name != "" && now.Sub(cp.At) < clauthFresh && len(cp.Limits) > 0 {
		a.Limits, a.At = cp.Limits, cp.At
		return key, a, 0
	}
	if err != nil {
		a.Err = err.Error()
		return key, a, 0
	}
	limits, err := fetchLimits(ctx, tok)
	var busy busyError
	if errors.As(err, &busy) {
		return key, a, busy.wait
	}
	if err != nil {
		a.Err = err.Error()
		return key, a, 0
	}
	a.Limits = limits
	return key, a, 0
}

// keychainLogin is ~/.claude's login on macOS, where it's in the Keychain.
const keychainLogin = "keychain:Claude Code-credentials"

var (
	errExpired = errors.New("login expired · renews when it's next used")
	errBusy    = errors.New("asked too often")
)

// busyError is a 429: the limits endpoint asked too often, by everyone
// using the account (each Claude Code session asks it too), and how long
// it says to wait.
type busyError struct{ wait time.Duration }

func (e busyError) Error() string        { return errBusy.Error() }
func (e busyError) Is(target error) bool { return target == errBusy }

// claudeToken is a login's access token, if it's still good. It's never
// renewed here: that's Claude Code's to do.
func claudeToken(login string, now time.Time) (string, error) {
	var data []byte
	var err error
	if service, ok := strings.CutPrefix(login, "keychain:"); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		data, err = exec.CommandContext(ctx, "security", "find-generic-password", "-s", service, "-w").Output()
	} else {
		data, err = os.ReadFile(login)
	}
	if err != nil {
		return "", errors.New("can't read the login")
	}
	var c struct {
		OAuth struct {
			Token     string `json:"accessToken"`
			ExpiresAt int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(data, &c) != nil || c.OAuth.Token == "" {
		return "", errors.New("not a Claude login")
	}
	if c.OAuth.ExpiresAt > 0 && time.UnixMilli(c.OAuth.ExpiresAt).Before(now) {
		return "", errExpired
	}
	return c.OAuth.Token, nil
}

// anthropicGet asks Anthropic's API, as Claude Code does, with a login's
// token: sent there and nowhere else.
func anthropicGet(ctx context.Context, path, token string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "herdr-vitals")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.New("couldn't reach Anthropic")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		wait := time.Minute
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			wait = time.Duration(s) * time.Second
		}
		return busyError{wait}
	}
	if resp.StatusCode != http.StatusOK {
		return errors.New("Anthropic said " + resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func fetchWho(ctx context.Context, token string) (who, error) {
	var p struct {
		Account struct {
			UUID  string `json:"uuid"`
			Email string `json:"email"`
		} `json:"account"`
		Organization struct {
			UUID string `json:"uuid"`
			Type string `json:"organization_type"`
			Tier string `json:"rate_limit_tier"`
		} `json:"organization"`
	}
	if err := anthropicGet(ctx, "/api/oauth/profile", token, &p); err != nil {
		return who{}, err
	}
	if p.Account.UUID == "" {
		return who{}, errors.New("no account")
	}
	return who{Account: p.Account.UUID + "/" + p.Organization.UUID, Name: p.Account.Email, Plan: planText(p.Organization.Tier, p.Organization.Type)}, nil
}

// planText is a plan as it's sold: "Max 20x", "Pro", "Team".
func planText(tier, orgType string) string {
	switch {
	case strings.Contains(tier, "max_20x"):
		return "Max 20x"
	case strings.Contains(tier, "max_5x"):
		return "Max 5x"
	case strings.Contains(orgType, "max"):
		return "Max"
	case strings.Contains(orgType, "pro"):
		return "Pro"
	case strings.Contains(orgType, "team"):
		return "Team"
	case strings.Contains(orgType, "enterprise"):
		return "Enterprise"
	}
	return ""
}

// fetchLimits is a login's limits now: the list api/oauth/usage gives,
// each with what it's for. Its other fields come and go by code name.
func fetchLimits(ctx context.Context, token string) ([]limitBar, error) {
	var u struct {
		Limits []struct {
			Kind     string    `json:"kind"`
			Percent  float64   `json:"percent"`
			Severity string    `json:"severity"`
			ResetsAt time.Time `json:"resets_at"`
			Active   bool      `json:"is_active"`
			Scope    *struct {
				Model *struct {
					Name string `json:"display_name"`
				} `json:"model"`
				Surface *struct {
					Name string `json:"display_name"`
				} `json:"surface"`
			} `json:"scope"`
		} `json:"limits"`
	}
	if err := anthropicGet(ctx, "/api/oauth/usage", token, &u); err != nil {
		return nil, err
	}
	var out []limitBar
	for _, l := range u.Limits {
		label := map[string]string{"session": "session", "weekly_all": "week"}[l.Kind]
		if l.Scope != nil && l.Scope.Model != nil && l.Scope.Model.Name != "" {
			label = l.Scope.Model.Name
		} else if l.Scope != nil && l.Scope.Surface != nil && l.Scope.Surface.Name != "" {
			label = l.Scope.Surface.Name
		}
		if label == "" {
			label = strings.ReplaceAll(l.Kind, "_", " ")
		}
		model := l.Scope != nil && l.Scope.Model != nil && l.Scope.Model.Name != ""
		out = append(out, limitBar{Label: clean(label, false), Percent: l.Percent, Severity: l.Severity, ResetsAt: l.ResetsAt, Active: l.Active, Model: model})
	}
	return out, nil
}

// clauthProfile is what clauth knows of one of its profiles.
type clauthProfile struct {
	Name   string
	Plan   string
	Limits []limitBar
	At     time.Time
}

// clauthProfiles is clauth's profiles, by their login, if clauth is
// installed: their names, and its own latest figures.
func clauthProfiles() map[string]clauthProfile {
	path := findProgram("clauth")
	if path == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "status", "--json").Output()
	if err != nil {
		return nil
	}
	var st struct {
		Profiles []struct {
			Name      string    `json:"name"`
			Provider  string    `json:"provider"`
			Tier      string    `json:"tier"`
			FetchedAt time.Time `json:"fetched_at"`
			Windows   []struct {
				Label    string    `json:"label"`
				Percent  float64   `json:"utilization_pct"`
				ResetsAt time.Time `json:"resets_at"`
			} `json:"windows"`
		} `json:"profiles"`
	}
	if json.Unmarshal(out, &st) != nil {
		return nil
	}
	home, _ := os.UserHomeDir()
	res := map[string]clauthProfile{}
	for _, p := range st.Profiles {
		if p.Provider != "" && p.Provider != "anthropic" {
			continue
		}
		login, err := filepath.EvalSymlinks(filepath.Join(home, ".clauth", "profiles", p.Name, "credentials.json"))
		if err != nil {
			continue
		}
		cp := clauthProfile{Name: clean(p.Name, false), Plan: clean(p.Tier, false), At: p.FetchedAt}
		for _, w := range p.Windows {
			label := map[string]string{"5h": "session", "7d": "week"}[w.Label]
			model := false
			if label == "" { // "7d fable": a week's, for one model
				label, model = strings.TrimPrefix(w.Label, "7d "), true
				label = strings.ToUpper(label[:1]) + label[1:]
			}
			cp.Limits = append(cp.Limits, limitBar{Label: clean(label, false), Percent: w.Percent, ResetsAt: w.ResetsAt, Model: model})
		}
		res[login] = cp
	}
	return res
}

// codexAccount is a Codex folder's account, from the freshest of its
// sessions' limits.
func codexAccount(home string, limits *codexLimits) account {
	name := "Codex"
	if def, _ := os.UserHomeDir(); home != filepath.Join(def, ".codex") {
		name += " (" + filepath.Base(home) + ")"
	}
	a := account{Key: "codex:" + home, Kind: "codex", Name: name}
	if limits == nil {
		a.Err = "no limits yet: they come with its next reply"
		return a
	}
	rl := limits.Limits
	a.At, a.Plan = limits.At, clean(rl.PlanType, false)
	for _, w := range []*codexWindow{rl.Primary, rl.Secondary} {
		if w == nil {
			continue
		}
		a.Limits = append(a.Limits, limitBar{Label: windowText(w.WindowMinutes), Percent: w.UsedPercent, ResetsAt: time.Unix(w.ResetsAt, 0)})
	}
	if c := rl.Credits; c != nil && c.HasCredits && !c.Unlimited {
		a.Credits = countText(int(c.Balance)) + " credits"
	}
	return a
}

// windowText is a limit's window, briefly: "5h", "week", "30d".
func windowText(minutes int) string {
	switch {
	case minutes == 5*60:
		return "session" // as Claude's five hours are
	case minutes == 7*24*60:
		return "week"
	case minutes >= 24*60 && minutes%(24*60) == 0:
		return itoa(minutes/(24*60)) + "d"
	case minutes >= 60:
		return itoa(minutes/60) + "h"
	}
	return itoa(minutes) + "m"
}

// resetText is when a limit resets, as you'd say it: "01:10" today,
// "Mon 19:00" this week, "26 Oct" after.
func resetText(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	// To the nearest minute: they come a moment before the hour.
	t, now = t.Add(30*time.Second).Truncate(time.Minute).Local(), now.Local()
	switch {
	case t.Sub(now) < 24*time.Hour && t.Day() == now.Day():
		return t.Format("15:04")
	case t.Sub(now) < 6*24*time.Hour:
		return t.Format("Mon 15:04")
	}
	return t.Format("2 Jan")
}

// agoText is how long ago, briefly: "just now", "4m ago", "19h ago".
func agoText(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case t.IsZero():
		return ""
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + "m ago"
	case d < 48*time.Hour:
		return itoa(int(d.Hours())) + "h ago"
	}
	return itoa(int(d.Hours()/24)) + "d ago"
}

// findProgram finds a program on the PATH, or where it's usually put: a
// herdr pane or hook often has a bare PATH, without ~/.local/bin.
func findProgram(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, dir := range []string{filepath.Join(home, ".local", "bin"), filepath.Join(home, ".cargo", "bin"),
		filepath.Join(home, "go", "bin"), "/usr/local/bin", "/opt/homebrew/bin"} {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}
