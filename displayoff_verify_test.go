//go:build windows

package main

import (
	"image"
	"os"
	"testing"
	"time"
)

func frameBrightness(img *image.RGBA) float64 {
	if img == nil {
		return -1
	}
	var sum, n float64
	for i := 0; i+2 < len(img.Pix); i += 64 {
		sum += float64(img.Pix[i]) + float64(img.Pix[i+1]) + float64(img.Pix[i+2])
		n += 3
	}
	return sum / n
}

// The production path with the panel powered off: a viewer arrives (which
// issues the wake and takes the display hold) and the first capture must
// succeed within the retry window instead of failing on the dark panel.
// Drives the real display — runs only when asked.
func TestFirstCaptureSurvivesDarkPanel(t *testing.T) {
	if os.Getenv("PCREMOTE_UI_TEST") == "" {
		t.Skip("set PCREMOTE_UI_TEST=1 to run (powers the real display off for ~8s)")
	}
	// Power the panel off (2 = off). Capture keeps working for a few seconds
	// after the command — the driver cuts off late — so wait for it to actually
	// break before testing the recovery.
	displayOff()
	broken := false
	for start := time.Now(); time.Since(start) < 12*time.Second; time.Sleep(500 * time.Millisecond) {
		if _, err := captureDisplay(0); err != nil {
			t.Logf("panel off, capture broke after %.1fs: %v", time.Since(start).Seconds(), err)
			broken = true
			break
		}
	}
	if !broken {
		t.Log("capture still works with the panel off — cannot reproduce on this machine, skipping")
		wakeDisplayNow()
		return
	}

	viewerArrived()
	defer viewerLeft()
	t0 := time.Now()
	img, err := captureAfterWake(0, 5*time.Second)
	if err != nil {
		t.Fatalf("first frame still failing after wake: %v", err)
	}
	t.Logf("captured after %.1fs, brightness=%.0f", time.Since(t0).Seconds(), frameBrightness(img))

	// And it keeps working while the viewer holds the display.
	time.Sleep(2 * time.Second)
	if _, err := captureDisplay(0); err != nil {
		t.Fatalf("panel went dark while a viewer is still connected: %v", err)
	}
}
