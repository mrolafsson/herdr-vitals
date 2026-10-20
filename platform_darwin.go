package main

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// processExe is the program a process runs, as ps has it.
func processExe(pid int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	return strings.TrimSpace(string(out)), err
}
