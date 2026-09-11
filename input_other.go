//go:build !windows

package main

import "log"

// Input injection is Windows-only for now; other platforms build but stay
// view-only so cross-compiling for a quick check still works.
func injectInput(ev InputEvent) {
	log.Printf("[pc-remote] input ignored (Windows only): %s", ev.Type)
}
