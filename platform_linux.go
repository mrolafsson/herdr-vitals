package main

import (
	"bytes"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// readProcessEnv reads /proc/<pid>/environ: readable for your own processes.
func readProcessEnv(pid int) (map[string]string, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, kv := range bytes.Split(data, []byte{0}) {
		if k, v, ok := bytes.Cut(kv, []byte("=")); ok {
			env[string(k)] = string(v)
		}
	}
	return env, nil
}

// processExe is the program a process runs.
func processExe(pid int) (string, error) {
	return os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
}

// openFiles lists what a process has open, from /proc/<pid>/fd.
func openFiles(pid int) []string {
	dir := "/proc/" + strconv.Itoa(pid) + "/fd"
	fds, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, fd := range fds {
		if target, err := os.Readlink(dir + "/" + fd.Name()); err == nil && strings.HasPrefix(target, "/") {
			files = append(files, target)
		}
	}
	return files
}

// samplerUnit starts the sampler as a systemd unit of its own, where
// systemd-run is: it outlives herdr-core's commands, which end what they
// leave behind. Nil elsewhere: started as is.
func samplerUnit(self, logPath string) *exec.Cmd {
	run, err := exec.LookPath("systemd-run")
	if err != nil || os.Getenv("HERDR_VITALS_NO_SYSTEMD") != "" {
		return nil
	}
	argv := []string{"--user", "--collect", "--quiet"}
	for _, kv := range os.Environ() {
		if !strings.ContainsAny(kv, "\n") {
			argv = append(argv, "--setenv="+kv)
		}
	}
	argv = append(argv, "--setenv=HERDR_VITALS_LOG="+logPath, "/bin/sh", "-c", `exec "$0" sampler >>"$HERDR_VITALS_LOG" 2>&1`, self)
	return exec.Command(run, argv...)
}
