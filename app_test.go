package main

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The app mod pins the version it downloads; this build says it's that one.
func TestAppVersionIsThePlugins(t *testing.T) {
	toml, err := os.ReadFile("herdr-plugin.toml")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^version = "([^"]+)"`).FindSubmatch(toml)
	if m == nil || string(m[1]) != appVersion {
		t.Fatalf("appVersion %q, herdr-plugin.toml %q", appVersion, m)
	}
	js, err := os.ReadFile("app/mods/vitals/main.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(js), `const VITALS_VERSION = "`+appVersion+`"`) {
		t.Fatalf("app/mods/vitals/main.js doesn't pin %s", appVersion)
	}
}

func appRun(t *testing.T, req string) map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := runApp(context.Background(), withDefaults(config{}), []string{req}, &out); err != nil {
		t.Fatal(err)
	}
	var res map[string]any
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	return res
}

// A demo machine reads as a real one would: each core, a couple of minutes
// to draw, its disks; a machine that's down says so.
func TestAppDemoReading(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	names := appRun(t, `{"op":"machines"}`)["machines"].([]any)
	if len(names) < 3 {
		t.Fatalf("machines %v", names)
	}
	r := appRun(t, `{"op":"now","demo":true,"machine":"atlas"}`)
	if r["host"] != "atlas" || r["warming"] != false || len(r["perCore"].([]any)) != int(r["cores"].(float64)) {
		t.Fatalf("atlas: %v %v %v", r["host"], r["warming"], r["cores"])
	}
	if len(r["history"].([]any)) < 100 || len(r["disks"].([]any)) == 0 {
		t.Fatalf("history %d, disks %d", len(r["history"].([]any)), len(r["disks"].([]any)))
	}
	for _, d := range demoMachines {
		if d.down != "" || d.auth != "" {
			if res := appRun(t, `{"op":"now","demo":true,"machine":"`+d.Name+`"}`); res["error"] == nil {
				t.Fatalf("%s should say it's not answering", d.Name)
			}
		}
	}
}

// A first reading has nothing to tell CPU from (on Linux); the next has
// each core.
func TestAppDescribeWarmsUp(t *testing.T) {
	d := demoMachines[0]
	st := &demoState{cores: make([]cpuTimes, d.cores), procs: make([]float64, len(d.procs))}
	a := d.round(st, newRand(), nowFixed(), 1e9)
	b := d.round(st, newRand(), nowFixed().Add(1e9), 1e9)
	if r := describe(a, nil, nil, nil); !r.Warming || len(r.PerCore) != 0 {
		t.Fatal("a first reading should be warming up")
	}
	if r := describe(b, a, nil, nil); r.Warming || len(r.PerCore) != d.cores || r.CPU <= 0 {
		t.Fatalf("second: warming %v cores %d cpu %v", r.Warming, len(r.PerCore), r.CPU)
	}
}

func newRand() *rand.Rand { return rand.New(rand.NewSource(1)) }
func nowFixed() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) }
