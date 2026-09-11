//go:build !windows

package main

// Power management is a Windows concern here; elsewhere viewing is
// best-effort anyway.
func viewerArrived()                 {}
func viewerLeft()                    {}
func keepAwake(policy func() string) {}
