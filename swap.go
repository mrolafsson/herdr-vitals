package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// Going from this popup to another plugin's without a blink: herdr shows one
// popup at a time, and closing one to open the next shows the panes under
// them for a moment. So this program becomes the other plugin's instead
// (exec), in the popup that's already open, with the environment herdr
// would have given it. The popup keeps the size and title it was opened with.

// pendingSwap is the popup to become once the terminal is the shell's again.
var pendingSwap *popupRef

// paneCommand is where herdr has a plugin's popup: its folder and command.
func paneCommand(p popupRef) (root string, command []string, err error) {
	var res struct {
		Plugins []struct {
			PluginID string `json:"plugin_id"`
			Enabled  bool   `json:"enabled"`
			Root     string `json:"plugin_root"`
			Panes    []struct {
				ID      string   `json:"id"`
				Command []string `json:"command"`
			} `json:"panes"`
		} `json:"plugins"`
	}
	if err := herdrCall("plugin.list", nil, &res); err != nil {
		return "", nil, err
	}
	for _, pl := range res.Plugins {
		if pl.PluginID != p.PluginID || !pl.Enabled {
			continue
		}
		for _, pane := range pl.Panes {
			if pane.ID == p.Entrypoint && len(pane.Command) > 0 && pl.Root != "" {
				return pl.Root, pane.Command, nil
			}
		}
	}
	return "", nil, errors.New(p.PluginID + " has no popup called " + p.Entrypoint)
}

// swapEnv is this process's environment made the other plugin's: herdr's
// variables for a plugin renamed for it, the ones this popup was opened with
// dropped, and the ones the other is opened with set.
func swapEnv(env []string, own string, p popupRef, root string) []string {
	var out []string
	dirs := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		switch {
		case k == "HERDR_PLUGIN_CONFIG_DIR" || k == "HERDR_PLUGIN_STATE_DIR":
			// herdr keeps each plugin's under its ID, side by side.
			if filepath.Base(v) == own {
				dirs[k] = filepath.Join(filepath.Dir(v), p.PluginID)
			}
		case strings.HasPrefix(k, "HERDR_PLUGIN_"), k == returnEnv, strings.HasPrefix(k, "HERDR_SNOOZE_"), strings.HasPrefix(k, "HERDR_VITALS_"),
			strings.HasPrefix(k, "HERDR_RECAP_"), strings.HasPrefix(k, "HERDR_GITHUB_"), strings.HasPrefix(k, "HERDR_LINEAR_"):
		default:
			out = append(out, kv)
		}
	}
	out = append(out, "HERDR_PLUGIN_ID="+p.PluginID, "HERDR_PLUGIN_ROOT="+root, "HERDR_PLUGIN_ENTRYPOINT_ID="+p.Entrypoint)
	for k, v := range dirs {
		out = append(out, k+"="+v)
	}
	for k, v := range p.Env {
		out = append(out, k+"="+v)
	}
	return out
}

// swapInto becomes the other plugin's popup. It only returns if it couldn't.
func swapInto(p popupRef) error {
	if !p.valid() {
		return errors.New("not a popup: " + p.PluginID + "/" + p.Entrypoint)
	}
	root, command, err := paneCommand(p)
	if err != nil {
		return err
	}
	path := command[0]
	if strings.Contains(path, "/") {
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
	} else if path, err = exec.LookPath(path); err != nil {
		return err
	}
	if err := os.Chdir(root); err != nil {
		return err
	}
	return syscall.Exec(path, command, swapEnv(os.Environ(), pluginID(), p, root))
}

// finishSwap is the last thing a popup does: become the popup it was closed
// for. If it can't, the popup just closes: that one is a key away.
func finishSwap() error {
	if p := pendingSwap; p != nil {
		return swapInto(*p)
	}
	return nil
}
