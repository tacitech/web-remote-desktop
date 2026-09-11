//go:build !windows

package main

import "os/exec"

// hideConsole is a no-op off Windows: there are no stray console windows there.
func hideConsole(cmd *exec.Cmd) {}
