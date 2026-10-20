package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// herdrError is a refusal from the herdr server, as opposed to a transport failure.
type herdrError struct {
	Code    string
	Message string
}

func (e *herdrError) Error() string { return e.Code + ": " + e.Message }

func socketPath() string {
	if p := os.Getenv("HERDR_SOCKET_PATH"); p != "" {
		return p
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "herdr", "herdr.sock")
}

// herdrCall sends one request and decodes `result` into out. The server answers
// a single line per connection and hangs up.
func herdrCall(method string, params any, out any) error {
	conn, err := net.DialTimeout("unix", socketPath(), 3*time.Second)
	if err != nil {
		return fmt.Errorf("herdr socket: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	if params == nil {
		params = map[string]any{}
	}
	req, err := json.Marshal(map[string]any{"id": "herdr-vitals", "method": method, "params": params})
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return err
	}
	line, err := bufio.NewReaderSize(conn, 1<<20).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return fmt.Errorf("herdr %s: %w", method, err)
	}
	var reply struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(line, &reply); err != nil {
		return fmt.Errorf("herdr %s: bad reply: %w", method, err)
	}
	if reply.Error != nil {
		return &herdrError{reply.Error.Code, reply.Error.Message}
	}
	if out != nil && len(reply.Result) > 0 {
		return json.Unmarshal(reply.Result, out)
	}
	return nil
}

func isHerdrCode(err error, code string) bool {
	var he *herdrError
	return errors.As(err, &he) && he.Code == code
}

func notify(title, body string) {
	_ = herdrCall("notification.show", map[string]any{"title": title, "body": body}, nil)
}

// openPopup opens one of this plugin's pane entrypoints as a popup. The menu the
// action was picked from may still be closing, so ui_busy is retried briefly.
func openPopup(entrypoint, width, height string, env map[string]string) error {
	params := map[string]any{
		"plugin_id": pluginID(), "entrypoint": entrypoint, "placement": "popup",
		"focus": true, "width": width, "height": height, "env": env,
	}
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		if err = herdrCall("plugin.pane.open", params, nil); err == nil || !isHerdrCode(err, "ui_busy") {
			return err
		}
		time.Sleep(150 * time.Millisecond)
	}
	return err
}

// openPane opens the monitor as a pane of its own beside the one you're in,
// leaving you where you were. herdr's split takes a direction and a share of
// the pane; one that doesn't is asked without them.
func openPane(entrypoint string, env map[string]string) error {
	if env == nil {
		env = map[string]string{} // herdr wants an object, if an empty one
	}
	params := map[string]any{
		"plugin_id": pluginID(), "entrypoint": entrypoint, "placement": "split",
		"direction": "right", "ratio": 0.4, "focus": false, "env": env,
	}
	if pane := os.Getenv("HERDR_PANE_ID"); pane != "" {
		params["target_pane_id"] = pane
	}
	err := herdrCall("plugin.pane.open", params, nil)
	if isHerdrCode(err, "invalid_params") || isHerdrCode(err, "invalid_request") {
		delete(params, "direction")
		delete(params, "ratio")
		err = herdrCall("plugin.pane.open", params, nil)
	}
	return err
}

// closePane closes one of this plugin's panes: whether it was there to close.
func closePane(paneID string) bool {
	return herdrCall("plugin.pane.close", map[string]any{"pane_id": paneID}, nil) == nil
}

// paneExists is whether a pane is still open.
func paneExists(paneID string) bool {
	return herdrCall("pane.get", map[string]any{"pane_id": paneID}, nil) == nil
}

// machine is one of the SSH machines saved in herdr.
type machine struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Target  string `json:"target"`
	Enabled bool   `json:"enabled"`
}

// herdrMachines is the machines saved in herdr, from `herdr machine list`:
// the catalog isn't on the socket. None if herdr can't say.
func herdrMachines() []machine {
	bin := os.Getenv("HERDR_BIN_PATH")
	if bin == "" {
		bin = "herdr"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "machine", "list", "--json").Output()
	if err != nil {
		return nil
	}
	var ms []machine
	if json.Unmarshal(out, &ms) != nil {
		return nil
	}
	sanitize(&ms)
	return ms
}
