//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// hideConsole stops a child console program (cloudflared, ffmpeg) from flashing
// a black window on the desktop. The parent is built as a GUI app so it has no
// console of its own; without this, each child creates one — which is precisely
// the "it keeps popping up" annoyance when this runs at logon.
func hideConsole(cmd *exec.Cmd) {
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}
