package main

import (
	"encoding/json"
	"os"
	"regexp"
)

// Going between this popup and the other herdr plugins' (herdr-recap opens
// this one from its own): herdr shows one popup at a time, so the one this
// was opened from says where to go back to when this one closes.

// returnEnv is how a popup is told where it was opened from: a popupRef, as
// JSON, to open again when it's closed.
const returnEnv = "HERDR_POPUP_RETURN"

// popupRef is a plugin's popup, as plugin.pane.open takes it.
type popupRef struct {
	PluginID   string            `json:"plugin_id"`
	Entrypoint string            `json:"entrypoint"`
	Width      string            `json:"width,omitempty"`
	Height     string            `json:"height,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
}

// A plugin's or an entrypoint's ID, as herdr allows them: what a popupRef
// read from the environment is held to.
var refID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func (p popupRef) valid() bool {
	return refID.MatchString(p.PluginID) && refID.MatchString(p.Entrypoint)
}

// cameFrom is the popup this one was opened from, if it was.
func cameFrom() (popupRef, bool) {
	var p popupRef
	if json.Unmarshal([]byte(os.Getenv(returnEnv)), &p) != nil || !p.valid() {
		return popupRef{}, false
	}
	return p, true
}
