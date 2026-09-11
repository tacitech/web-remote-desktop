package main

import (
	"fmt"
	"image"
	"time"

	"github.com/kbinani/screenshot"
)

// numDisplays returns how many displays are attached.
func numDisplays() int {
	n := screenshot.NumActiveDisplays()
	if n < 1 {
		return 1
	}
	return n
}

// displayBounds returns the pixel bounds of display i (empty-safe).
func displayBounds(i int) image.Rectangle {
	if i < 0 || i >= screenshot.NumActiveDisplays() {
		i = 0
	}
	return screenshot.GetDisplayBounds(i)
}

// captureDisplay grabs display i as an RGBA image.
func captureDisplay(i int) (*image.RGBA, error) {
	img, _, err := captureDisplayChanged(i)
	return img, err
}

// captureDisplayChanged grabs display i and reports whether the picture differs
// from the previous grab. Desktop Duplication answers that for free — a still
// screen costs nothing at all — so callers that can skip work on an unchanged
// frame should use this. The GDI path can't tell, so it always says "changed".
func captureDisplayChanged(i int) (*image.RGBA, bool, error) {
	if img, changed, err := captureDisplayFast(i); err == nil {
		return img, changed, nil
	}
	img, err := captureDisplayGDI(i)
	return img, true, err
}

// captureDisplayGDI is the portable fallback: it copies the whole desktop
// through the CPU, which is slow but works when duplication is unavailable
// (secure desktop, a UAC prompt, a monitor on another GPU, session switch).
func captureDisplayGDI(i int) (*image.RGBA, error) {
	b := displayBounds(i)
	if b.Empty() {
		return nil, fmt.Errorf("display %d empty bounds", i)
	}
	return screenshot.CaptureRect(b)
}

// captureAfterWake retries the first capture while the display wakes up.
//
// With the panel powered off, BitBlt fails and duplication has nothing to hand
// over — capture returns an error, not a dark frame. The wake issued when the
// viewer arrived takes a second or two to land. Giving up on the first error
// closed the socket, which released the display hold, which let the panel go
// dark again — and the phone reconnected into the same wall, forever.
func captureAfterWake(i int, wait time.Duration) (*image.RGBA, error) {
	deadline := time.Now().Add(wait)
	for {
		img, err := captureDisplay(i)
		if err == nil || time.Now().After(deadline) {
			return img, err
		}
		time.Sleep(250 * time.Millisecond)
	}
}
