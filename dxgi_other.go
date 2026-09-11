//go:build !windows

package main

import (
	"fmt"
	"image"
)

// Desktop Duplication is a Windows API; elsewhere the GDI-equivalent path is
// the only one, so this always defers to it.
func captureDisplayFast(idx int) (*image.RGBA, bool, error) {
	return nil, false, fmt.Errorf("not supported on this OS")
}
