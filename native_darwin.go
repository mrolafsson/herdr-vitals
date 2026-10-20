package main

import (
	"context"
	"time"
)

// On macOS this machine is read with the probe, as any other: there's no
// /proc to read it from.
func nativeLite() bool { return false }

func watchNative(context.Context, int, time.Duration, chan<- frameMsg) {}
