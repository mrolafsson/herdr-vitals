package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type config struct {
	// Hosts are more machines to watch, over ssh: each a name to show and an
	// ssh target ("gpu-box", "me@10.0.0.4", "ssh://me@host:2222"), or just
	// the target, which is then its name too.
	Hosts []hostConfig `json:"hosts"`
	// HerdrMachines watches the machines saved in herdr (`herdr machine
	// list`), as they're named there. On by default.
	HerdrMachines *bool `json:"herdr_machines"`
	// Local watches the machine herdr runs on. On by default.
	Local *bool `json:"local"`
	// Background keeps a sampler running (started with herdr), so the
	// monitor opens on full graphs and has a day to look back on. On by
	// default.
	Background *bool `json:"background"`
	// Alerts toasts when a machine runs low on CPU, memory or disk; needs
	// Background. On by default.
	Alerts *bool `json:"alerts"`
	// IntervalSeconds is how often each machine is read. Default 2.
	IntervalSeconds float64 `json:"interval_seconds"`
	// Theme: "dark", "light", or empty to ask the terminal.
	Theme string `json:"theme"`
}

type hostConfig struct {
	Name string `json:"name"`
	SSH  string `json:"ssh"`
}

// UnmarshalJSON takes a host as an object or as just its ssh target.
func (h *hostConfig) UnmarshalJSON(data []byte) error {
	var target string
	if json.Unmarshal(data, &target) == nil {
		*h = hostConfig{SSH: target}
		return nil
	}
	type plain hostConfig
	return json.Unmarshal(data, (*plain)(h))
}

func (c config) interval() time.Duration {
	return time.Duration(c.IntervalSeconds * float64(time.Second))
}

func pluginID() string {
	if id := os.Getenv("HERDR_PLUGIN_ID"); id != "" {
		return id
	}
	return "herdr-vitals"
}

func xdgDir(env, fallback string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, fallback)
}

func configDir() string {
	if d := os.Getenv("HERDR_PLUGIN_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(xdgDir("XDG_CONFIG_HOME", ".config"), "herdr", "plugins", "config", pluginID())
}

func stateDir() string {
	if d := os.Getenv("HERDR_PLUGIN_STATE_DIR"); d != "" {
		return d
	}
	return filepath.Join(xdgDir("XDG_STATE_HOME", ".local/state"), "herdr", "plugins", pluginID())
}

// loadConfig reads config.json. On error it still returns the defaults.
func loadConfig() (config, error) {
	cfg := config{}
	data, err := os.ReadFile(filepath.Join(configDir(), "config.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return withDefaults(config{}), err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return withDefaults(config{}), errors.New("config.json: " + err.Error())
		}
	}
	return withDefaults(cfg), nil
}

func withDefaults(cfg config) config {
	on := true
	if cfg.HerdrMachines == nil {
		cfg.HerdrMachines = &on
	}
	if cfg.Local == nil {
		cfg.Local = &on
	}
	if cfg.Background == nil {
		cfg.Background = &on
	}
	if cfg.Alerts == nil {
		cfg.Alerts = &on
	}
	// Faster than half a second is the probe watching itself.
	if cfg.IntervalSeconds < 0.5 {
		cfg.IntervalSeconds = 2
	}
	for i, h := range cfg.Hosts {
		cfg.Hosts[i].SSH = strings.TrimSpace(h.SSH)
		if strings.TrimSpace(h.Name) == "" {
			cfg.Hosts[i].Name = sshHostName(cfg.Hosts[i].SSH)
		}
	}
	return cfg
}
