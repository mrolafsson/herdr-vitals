package main

import (
	"os"
	"strconv"
)

// processExe is the program a process runs.
func processExe(pid int) (string, error) {
	return os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
}
