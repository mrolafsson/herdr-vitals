package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A Claude reply is logged a line a content block, each with the whole
// reply's usage: it's counted once. A line still being written waits.
func TestClaudeUsageCountsEachReplyOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	reply := func(id, at string) string {
		return `{"type":"assistant","timestamp":"` + at + `","message":{"id":"` + id + `","model":"claude-opus-5-5","usage":{"input_tokens":10,"output_tokens":200,"cache_creation_input_tokens":1000,"cache_read_input_tokens":50000}}}` + "\n"
	}
	now := time.Now().UTC()
	at := now.Add(-time.Minute).Format(time.RFC3339)
	os.WriteFile(path, []byte(reply("a", at)+reply("a", at)+`{"type":"user","message":{"content":"hi"}}`+"\n"+reply("b", at)+`{"type":"assis`), 0o600)
	tl := newTally(path)
	tl.update()
	if tl.Out != 400 || tl.CacheRead != 100000 || tl.work() != 2420 || modelText(tl.Model) != "Opus 5.5" {
		t.Fatalf("out %d read %d work %d model %q", tl.Out, tl.CacheRead, tl.work(), tl.Model)
	}
	if r := tl.rate(now, 5); r != 2420.0/5 {
		t.Errorf("rate %v", r)
	}
	// The rest of the line comes: then it's read, and only it.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`tant","timestamp":"` + at + `","message":{"id":"c","usage":{"output_tokens":5}}}` + "\n")
	f.Close()
	tl.update()
	if tl.Out != 405 {
		t.Errorf("out %d after the line was finished", tl.Out)
	}
}

func TestCodexUsageAndLimits(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions", "2026", "10", "20")
	os.MkdirAll(dir, 0o700)
	path := filepath.Join(dir, "rollout-2026-10-20T19-47-05-01a15a6c-8d10-7f53-aab4-81b4796f4c53.jsonl")
	os.WriteFile(path, []byte(`{"timestamp":"2026-10-20T18:40:40Z","type":"turn_context","payload":{"model":"gpt-6-astra"}}
{"timestamp":"2026-10-20T18:40:46Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":101038,"cached_input_tokens":73472,"output_tokens":326},"last_token_usage":{"input_tokens":43436,"cached_input_tokens":33408,"output_tokens":106}},"rate_limits":{"primary":{"used_percent":15.0,"window_minutes":10080,"resets_at":1793083884},"secondary":null,"credits":{"has_credits":true,"unlimited":false,"balance":"72929.1093750000"},"plan_type":"prolite","something_new":{"a":1}}}}
`), 0o600)
	tl := newTally(path)
	tl.update()
	if tl.Model != "gpt-6-astra" || tl.In != 101038-73472 || tl.CacheRead != 73472 || tl.Out != 326 {
		t.Fatalf("%+v", tl)
	}
	if tl.Codex == nil || tl.Codex.Limits.Primary.UsedPercent != 15 {
		t.Fatalf("limits %+v", tl.Codex)
	}
	a := codexAccount("/somewhere/.codex", tl.Codex)
	if a.Plan != "prolite" || a.Credits != "72k credits" || len(a.Limits) != 1 || a.Limits[0].Label != "week" {
		t.Errorf("%+v", a)
	}
	if codexSessionID(path) != "01a15a6c-8d10-7f53-aab4-81b4796f4c53" {
		t.Error("codexSessionID")
	}
}

// The limits are read from the list /usage gives, whatever else comes
// with it; the token goes in the header and nowhere else.
func TestClaudeLimitsFromTheUsageList(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization") + " " + r.Header.Get("anthropic-beta")
		switch r.URL.Path {
		case "/api/oauth/usage":
			w.Write([]byte(`{"five_hour":{"utilization":6},"iguana_necktie":null,"limits":[
				{"kind":"session","percent":6,"severity":"normal","resets_at":"2026-10-20T23:10:00.38Z","scope":null,"is_active":false},
				{"kind":"weekly_all","percent":11,"severity":"normal","resets_at":"2026-10-26T17:00:00Z","is_active":true},
				{"kind":"weekly_scoped","percent":4,"severity":"warning","resets_at":"2026-10-26T17:00:00Z","scope":{"model":{"id":null,"display_name":"Fable"}}}]}`))
		case "/api/oauth/profile":
			w.Write([]byte(`{"account":{"uuid":"acc","email":"me@example.com"},"organization":{"uuid":"org","organization_type":"claude_max","rate_limit_tier":"default_claude_max_20x"}}`))
		default:
			w.Header().Set("Retry-After", "63")
			w.WriteHeader(http.StatusTooManyRequests)
		}
	}))
	defer srv.Close()
	was := apiBase
	apiBase = srv.URL
	defer func() { apiBase = was }()
	ls, err := fetchLimits(context.Background(), "tok")
	if err != nil || len(ls) != 3 || ls[0].Label != "session" || ls[1].Label != "week" || !ls[1].Active || ls[2].Label != "Fable" {
		t.Fatalf("%+v %v", ls, err)
	}
	if got != "Bearer tok oauth-2025-04-20" {
		t.Errorf("sent %q", got)
	}
	if limitLevel(ls[2]) != levelWarn || limitLevel(limitBar{Percent: 96}) != levelHot {
		t.Error("limitLevel")
	}
	w, err := fetchWho(context.Background(), "tok")
	if err != nil || w.Account != "acc/org" || w.Plan != "Max 20x" {
		t.Errorf("%+v %v", w, err)
	}
	var busy busyError
	if err := anthropicGet(context.Background(), "/elsewhere", "tok", &struct{}{}); !errors.As(err, &busy) || busy.wait != 63*time.Second {
		t.Errorf("a 429 is %v", err)
	}
}

// A login whose token has run out isn't sent, and isn't renewed.
func TestAnExpiredLoginIsLeftAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	os.WriteFile(path, []byte(`{"claudeAiOauth":{"accessToken":"tok","expiresAt":1000}}`), 0o600)
	if _, err := claudeToken(path, time.Now()); err != errExpired {
		t.Errorf("%v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"expiresAt":1000`) {
		t.Error("the login was changed")
	}
}

func TestLimitToasts(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	var said []string
	a := loadAlerts()
	a.notify = func(_, body string) { said = append(said, body) }
	now := time.Now()
	acct := func(p float64) []account {
		return []account{{Key: "k", Name: "tvh", Limits: []limitBar{{Label: "week", Percent: p, ResetsAt: now.Add(48 * time.Hour)}}}}
	}
	a.checkAccounts(acct(82), now)
	a.checkAccounts(acct(85), now)
	a.checkAccounts(acct(97), now)
	a.checkAccounts(acct(100), now)
	if len(said) != 2 || !strings.Contains(said[0], "tvh: weekly limit at 82%") || !strings.Contains(said[1], "97%") {
		t.Fatalf("%q", said)
	}
	a.checkAccounts(acct(5), now) // reset
	a.checkAccounts(acct(99), now)
	if len(said) != 3 {
		t.Errorf("after a reset: %q", said)
	}
}

func TestHowThingsAreSaid(t *testing.T) {
	for in, want := range map[string]string{"claude-opus-5-5": "Opus 5.5", "claude-sonnet-5-5-20261001": "Sonnet 5.5",
		"claude-haiku-5-5": "Haiku 5.5", "gpt-6-astra": "gpt-6-astra"} {
		if got := modelText(in); got != want {
			t.Errorf("modelText(%q) = %q", in, got)
		}
	}
	if windowText(10080) != "week" || windowText(300) != "session" || windowText(43200) != "30d" {
		t.Error("windowText")
	}
	now := time.Date(2026, 10, 20, 20, 0, 0, 0, time.Local)
	if r := resetText(now.Add(2*time.Hour+59*time.Minute+59*time.Second), now); r != "23:00" {
		t.Errorf("resetText today %q", r)
	}
	if r := resetText(now.Add(20*24*time.Hour), now); !strings.Contains(r, "Nov") {
		t.Errorf("resetText later %q", r)
	}
	if countText(37_175_000) != "37M" || countText(1_636_607_000) != "1.6B" || countText(2_072_000) != "2.1M" {
		t.Error("countText")
	}
}
