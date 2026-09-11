//go:build windows

package main

import "syscall"

// Windows hands DPI-unaware processes a virtualized, scaled-down desktop: on a
// 150% display, SetCursorPos clamps at 2/3 of the real resolution, so anything
// in the bottom/right third of the screen is unreachable — clicks land in the
// wrong place and look like "nothing happened".
//
// Screen capture reports true pixels, so the process must speak true pixels
// too. Declare per-monitor DPI awareness before any capture or input happens.
func init() {
	const perMonitorAwareV2 = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4)

	u32 := syscall.NewLazyDLL("user32.dll")
	if p := u32.NewProc("SetProcessDpiAwarenessContext"); p.Find() == nil {
		if r, _, _ := p.Call(perMonitorAwareV2); r != 0 {
			return
		}
	}
	// Windows 8.1: PROCESS_PER_MONITOR_DPI_AWARE = 2
	if sh := syscall.NewLazyDLL("shcore.dll"); sh.Load() == nil {
		if p := sh.NewProc("SetProcessDpiAwareness"); p.Find() == nil {
			if r, _, _ := p.Call(2); r == 0 {
				return
			}
		}
	}
	// Last resort (Vista+): system-wide awareness.
	if p := u32.NewProc("SetProcessDPIAware"); p.Find() == nil {
		p.Call()
	}
}
