package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// demoModel is the monitor on the demo's machines, with their history in,
// width by height.
func demoModel(t *testing.T, md mode, width, height int) model {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cfg := withDefaults(config{})
	m := newModel(ctx, cfg, md, true)
	// Every machine's four minutes of history, then their first live round.
	want := (len(demoMachines)-1)*historyLen + 1
	for got := 0; got < want; {
		select {
		case f := <-m.frames:
			m.apply(f)
			if f.sample != nil || f.status == "auth" {
				got++
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("the demo sent %d rounds of %d", got, want)
		}
	}
	m.beat = false
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return next.(model)
}

// TestWriteDemoScreens saves each README screen, as the monitor draws it,
// from the demo's machines. It only runs for scripts/screenshots.sh, which
// sets SCREENS_DIR and turns the files into PNGs with freeze.
func TestWriteDemoScreens(t *testing.T) {
	dir := os.Getenv("SCREENS_DIR")
	if dir == "" {
		t.Skip("set SCREENS_DIR to write the README screenshots")
	}
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })
	// herdr's default theme, which freeze's background matches.
	darkTerminal = true
	useTheme(func() *palette { p := herdrPalettes["catppuccin"]; return &p }())
	t.Cleanup(func() { useTheme(nil) })
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	save := func(name string, m model) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name+".ansi"), []byte(m.View()+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	grid := demoModel(t, modePopup, 168, 46)
	save("grid", grid)

	detail := grid
	detail.cursor = 1
	detail.detail = true
	save("detail", detail)

	day := detail
	day.day = true
	day.readDay()
	save("day", day)

	procs := grid
	procs.procs = true
	save("procs", procs)

	pane := demoModel(t, modePane, 62, 60)
	save("pane", pane)

	strip := demoModel(t, modePane, 190, 2)
	save("strip", strip)
}
