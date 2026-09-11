//go:build windows

package main

import (
	"image"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// wakeDisplay powers the monitor back on and asks Windows to keep it on. With
// the display asleep the compositor presents nothing, so every capture path —
// duplication and GDI alike — sees an unchanging black screen.
func wakeDisplay() {
	user32d := syscall.NewLazyDLL("user32.dll")
	user32d.NewProc("SendMessageW").Call(0xffff, 0x0112, 0xF170, ^uintptr(0))
	syscall.NewLazyDLL("kernel32.dll").NewProc("SetThreadExecutionState").Call(0x80000000 | 0x2 | 0x1)
	moveCursorNorm(0.5, 0.5)
	time.Sleep(1200 * time.Millisecond)
}

// This test drives the real desktop — it wakes the monitor, opens Notepad and
// types into it — so it only runs when asked for explicitly.
func TestCaptureVerify(t *testing.T) {
	if os.Getenv("PCREMOTE_UI_TEST") == "" {
		t.Skip("set PCREMOTE_UI_TEST=1 to run (drives the real display)")
	}
	wakeDisplay()

	// Notepad opening, drawing and closing gives the desktop something real to
	// present, which is what duplication reports on.
	np := exec.Command("notepad.exe")
	if err := np.Start(); err != nil {
		t.Fatalf("notepad: %v", err)
	}
	defer func() { _ = np.Process.Kill() }()
	time.Sleep(1200 * time.Millisecond)

	if _, _, err := captureDisplayFast(0); err != nil {
		t.Fatalf("DXGI unavailable: %v", err)
	}

	var idle, moved time.Duration
	var nIdle, nMoved int
	var lastChanged *image.RGBA
	for i := 0; i < 60; i++ {
		// Typing keeps the window repainting.
		if i%3 == 0 {
			typeText("a", InputEvent{})
		}
		s := time.Now()
		img, ch, err := captureDisplayFast(0)
		d := time.Since(s)
		if err != nil {
			t.Fatalf("fast: %v", err)
		}
		if ch {
			moved += d
			nMoved++
			lastChanged = img
		} else {
			idle += d
			nIdle++
		}
		time.Sleep(30 * time.Millisecond)
	}

	var gdi time.Duration
	for i := 0; i < 10; i++ {
		s := time.Now()
		if _, err := captureDisplayGDI(0); err != nil {
			t.Fatalf("gdi: %v", err)
		}
		gdi += time.Since(s)
	}
	avg := func(d time.Duration, n int) float64 {
		if n == 0 {
			return 0
		}
		return float64(d.Microseconds()) / float64(n) / 1000
	}
	t.Logf("DXGI still: %.2f ms x%d | DXGI changed: %.2f ms x%d | GDI: %.2f ms",
		avg(idle, nIdle), nIdle, avg(moved, nMoved), nMoved, avg(gdi, 10))

	if nMoved == 0 {
		t.Fatal("no changed frame to check")
	}

	// A wrong pitch or swapped colour channel would show up as a picture that
	// disagrees with the GDI one almost everywhere.
	gdiImg, err := captureDisplayGDI(0)
	if err != nil {
		t.Fatal(err)
	}
	if lastChanged.Bounds() != gdiImg.Bounds() {
		t.Fatalf("size mismatch: %v vs %v", lastChanged.Bounds(), gdiImg.Bounds())
	}
	diff, total := 0, 0
	b := gdiImg.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y += 4 {
		for x := b.Min.X; x < b.Max.X; x += 4 {
			c1, c2 := lastChanged.RGBAAt(x, y), gdiImg.RGBAAt(x, y)
			total++
			if c1.R != c2.R || c1.G != c2.G || c1.B != c2.B {
				diff++
			}
		}
	}
	t.Logf("differs from GDI: %d/%d samples (%.1f%%)", diff, total, 100*float64(diff)/float64(total))
	if float64(diff) > 0.2*float64(total) {
		t.Fatalf("too many pixels differ")
	}
}
