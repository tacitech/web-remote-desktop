//go:build windows

package main

import (
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// Mouse/keyboard injection via the Win32 SendInput API, called through
// syscall so the binary stays CGo-free (plain `go build`, no toolchain setup).

var (
	user32          = syscall.NewLazyDLL("user32.dll")
	procSendInput   = user32.NewProc("SendInput")
	procSetCursor   = user32.NewProc("SetCursorPos")
	procGetSysMetri = user32.NewProc("GetSystemMetrics")
)

const (
	inputMouse    = 0
	inputKeyboard = 1

	mouseeventfMove       = 0x0001
	mouseeventfLeftDown   = 0x0002
	mouseeventfLeftUp     = 0x0004
	mouseeventfRightDown  = 0x0008
	mouseeventfRightUp    = 0x0010
	mouseeventfMiddleDown = 0x0020
	mouseeventfMiddleUp   = 0x0040
	mouseeventfWheel      = 0x0800
	mouseeventfAbsolute   = 0x8000
	mouseeventfVirtualDsk = 0x4000

	keyeventfKeyup   = 0x0002
	keyeventfUnicode = 0x0004

	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79
)

type mouseInput struct {
	dx          int32
	dy          int32
	mouseData   uint32
	dwFlags     uint32
	time        uint32
	dwExtraInfo uintptr
}

type keybdInput struct {
	wVk         uint16
	wScan       uint16
	dwFlags     uint32
	time        uint32
	dwExtraInfo uintptr
	_           [8]byte // pad to MOUSEINPUT size inside the INPUT union
}

type input struct {
	inputType uint32
	_         uint32 // union alignment on amd64
	mi        mouseInput
}

func sendInputs(ins []input) {
	if len(ins) == 0 {
		return
	}
	procSendInput.Call(
		uintptr(len(ins)),
		uintptr(unsafe.Pointer(&ins[0])),
		unsafe.Sizeof(ins[0]),
	)
}

func sysMetric(i int) int32 {
	r, _, _ := procGetSysMetri.Call(uintptr(i))
	return int32(r)
}

// moveCursorNorm maps a normalized point on the streamed display to absolute
// desktop pixels. SetCursorPos takes plain virtual-desktop coordinates, which
// keeps multi-monitor and DPI scaling correct without the 0..65535 conversion
// SendInput's absolute mode needs.
func moveCursorNorm(nx, ny float64) {
	b := displayBounds(cfg.Monitor)
	if b.Empty() {
		return
	}
	x := b.Min.X + int(nx*float64(b.Dx()))
	y := b.Min.Y + int(ny*float64(b.Dy()))
	procSetCursor.Call(uintptr(int32(x)), uintptr(int32(y)))
}

func mouseFlags(button string, down bool) uint32 {
	switch strings.ToLower(button) {
	case "right":
		if down {
			return mouseeventfRightDown
		}
		return mouseeventfRightUp
	case "middle":
		if down {
			return mouseeventfMiddleDown
		}
		return mouseeventfMiddleUp
	default:
		if down {
			return mouseeventfLeftDown
		}
		return mouseeventfLeftUp
	}
}

func mouseEvent(flags uint32, data uint32) input {
	return input{
		inputType: inputMouse,
		mi:        mouseInput{dwFlags: flags, mouseData: data},
	}
}

func keyEvent(vk uint16, up bool) input {
	var in input
	in.inputType = inputKeyboard
	ki := keybdInput{wVk: vk}
	if up {
		ki.dwFlags = keyeventfKeyup
	}
	*(*keybdInput)(unsafe.Pointer(&in.mi)) = ki
	return in
}

// unicodeEvent types one UTF-16 code unit, bypassing keyboard layout entirely
// (so Vietnamese and symbols type correctly regardless of the PC's layout).
func unicodeEvent(ch uint16, up bool) input {
	var in input
	in.inputType = inputKeyboard
	ki := keybdInput{wScan: ch, dwFlags: keyeventfUnicode}
	if up {
		ki.dwFlags |= keyeventfKeyup
	}
	*(*keybdInput)(unsafe.Pointer(&in.mi)) = ki
	return in
}

// Virtual-key codes for the named keys the browser sends.
var vkByName = map[string]uint16{
	"Enter": 0x0D, "Backspace": 0x08, "Tab": 0x09, "Escape": 0x1B, "Space": 0x20,
	"ArrowUp": 0x26, "ArrowDown": 0x28, "ArrowLeft": 0x25, "ArrowRight": 0x27,
	"Home": 0x24, "End": 0x23, "PageUp": 0x21, "PageDown": 0x22, "Delete": 0x2E,
	"F1": 0x70, "F2": 0x71, "F3": 0x72, "F4": 0x73, "F5": 0x74, "F6": 0x75,
	"F7": 0x76, "F8": 0x77, "F9": 0x78, "F10": 0x79, "F11": 0x7A, "F12": 0x7B,
}

const (
	vkControl = 0x11
	vkMenu    = 0x12 // Alt
	vkShift   = 0x10
	vkWin     = 0x5B
)

// injectInput applies one browser event to the local desktop.
func injectInput(ev InputEvent) {
	switch ev.Type {
	case "move":
		moveCursorNorm(ev.X, ev.Y)

	case "down", "up":
		moveCursorNorm(ev.X, ev.Y)
		sendInputs([]input{mouseEvent(mouseFlags(ev.Button, ev.Type == "down"), 0)})

	case "click", "dblclick":
		moveCursorNorm(ev.X, ev.Y)
		down := mouseEvent(mouseFlags(ev.Button, true), 0)
		up := mouseEvent(mouseFlags(ev.Button, false), 0)
		sendInputs([]input{down, up})
		if ev.Type == "dblclick" {
			time.Sleep(30 * time.Millisecond)
			sendInputs([]input{down, up})
		}

	case "scroll":
		moveCursorNorm(ev.X, ev.Y)
		// Positive delta scrolls up, matching WHEEL_DELTA units.
		sendInputs([]input{mouseEvent(mouseeventfWheel, uint32(int32(ev.Delta)))})

	case "key":
		vk, ok := vkByName[ev.Key]
		if !ok {
			// Single printable character: type it as unicode.
			if len([]rune(ev.Key)) == 1 {
				typeText(ev.Key, ev)
			}
			return
		}
		var seq []input
		seq = append(seq, modifierDowns(ev)...)
		seq = append(seq, keyEvent(vk, false), keyEvent(vk, true))
		seq = append(seq, modifierUps(ev)...)
		sendInputs(seq)

	case "text":
		typeText(ev.Text, ev)
	}
}

// typeText sends literal text as unicode key events, honoring modifiers so
// shortcuts like Ctrl+C (text "c", ctrl=true) work.
func typeText(s string, ev InputEvent) {
	if s == "" {
		return
	}
	var seq []input
	seq = append(seq, modifierDowns(ev)...)
	for _, u := range utf16Units(s) {
		seq = append(seq, unicodeEvent(u, false), unicodeEvent(u, true))
	}
	seq = append(seq, modifierUps(ev)...)
	sendInputs(seq)
}

func modifierDowns(ev InputEvent) []input {
	var out []input
	if ev.Ctrl {
		out = append(out, keyEvent(vkControl, false))
	}
	if ev.Alt {
		out = append(out, keyEvent(vkMenu, false))
	}
	if ev.Shift {
		out = append(out, keyEvent(vkShift, false))
	}
	if ev.Meta {
		out = append(out, keyEvent(vkWin, false))
	}
	return out
}

func modifierUps(ev InputEvent) []input {
	var out []input
	if ev.Meta {
		out = append(out, keyEvent(vkWin, true))
	}
	if ev.Shift {
		out = append(out, keyEvent(vkShift, true))
	}
	if ev.Alt {
		out = append(out, keyEvent(vkMenu, true))
	}
	if ev.Ctrl {
		out = append(out, keyEvent(vkControl, true))
	}
	return out
}

func utf16Units(s string) []uint16 {
	return syscall.StringToUTF16(s)[:len(syscall.StringToUTF16(s))-1] // drop NUL
}
