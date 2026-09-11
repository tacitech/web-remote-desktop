//go:build windows

package main

import (
	"log"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Two power problems, two holds.
//
// The pitch-black picture: Windows powers the display down, the compositor
// stops presenting, and every capture path sees darkness. While somebody is
// watching, wake the panel and keep it awake; when the last viewer leaves, the
// power plan gets its display back.
//
// The page that won't load at all: the machine went to sleep. Modern Standby
// freezes desktop apps and drops their connections — the tunnel dies, and
// nothing on the phone can reach a sleeping PC, let alone wake it. A remote
// desktop host has to stay awake to be one. So hold the system awake — the
// display may still turn off — while the policy says so: on mains power by
// default, since a laptop running on battery in a bag should be allowed to
// sleep.

const (
	wkHwndBroadcast  = 0xffff
	wkWmSyscommand   = 0x0112
	wkScMonitorPower = 0xF170

	wkEsContinuous      = 0x80000000
	wkEsSystemRequired  = 0x00000001
	wkEsDisplayRequired = 0x00000002

	// How often the system hold re-checks the power source. Also the bound on
	// how long a hold outlives a policy change.
	keepAwakeInterval = 30 * time.Second
)

var (
	wkUser32               = syscall.NewLazyDLL("user32.dll")
	wkProcSendNotifyMsg    = wkUser32.NewProc("SendNotifyMessageW")
	wkKernel32             = syscall.NewLazyDLL("kernel32.dll")
	wkProcSetThreadExecSt  = wkKernel32.NewProc("SetThreadExecutionState")
	wkProcGetSysPowerStatu = wkKernel32.NewProc("GetSystemPowerStatus")
)

var (
	viewMu    sync.Mutex
	viewCount int
	viewStop  chan struct{}
)

// viewerArrived is called when a picture stream connects. The first viewer
// wakes the display and starts the hold; later ones just count.
func viewerArrived() {
	viewMu.Lock()
	defer viewMu.Unlock()
	viewCount++
	if viewCount == 1 {
		viewStop = make(chan struct{})
		go holdDisplay(viewStop)
	}
	// Broadcasts can stall on a hung window; do it off the request path.
	go wakeDisplayNow()
}

func viewerLeft() {
	viewMu.Lock()
	defer viewMu.Unlock()
	viewCount--
	if viewCount == 0 {
		close(viewStop)
	}
}

// wakeDisplayNow powers the panel back on if it was off, two ways at once.
//
// SC_MONITORPOWER(-1) is the documented request, posted with SendNotifyMessage
// so it never waits on other windows — a SendMessageTimeout broadcast was
// measured stalling for tens of seconds on a busy desktop, long after the
// viewer had given up. A zero-delta mouse move is the belt to that brace: any
// injected input counts as user activity, which is what actually wakes the
// panel on current Windows, and dx=dy=0 leaves the cursor where it was.
func wakeDisplayNow() {
	wkProcSendNotifyMsg.Call(wkHwndBroadcast, wkWmSyscommand, wkScMonitorPower, ^uintptr(0))
	sendInputs([]input{{inputType: inputMouse, mi: mouseInput{dwFlags: mouseeventfMove}}})
}

// displayOff powers the panel down (2 = off); used by tests to reproduce the
// dark-panel path.
func displayOff() {
	wkProcSendNotifyMsg.Call(wkHwndBroadcast, wkWmSyscommand, wkScMonitorPower, 2)
}

// holdDisplay keeps the panel from idling off for as long as stop is open.
// Execution state is per-thread, so the goroutine pins its thread and the
// state dies with it.
func holdDisplay(stop <-chan struct{}) {
	runtime.LockOSThread()
	wkProcSetThreadExecSt.Call(wkEsContinuous | wkEsDisplayRequired)
	<-stop
	wkProcSetThreadExecSt.Call(wkEsContinuous)
}

// wantSystemHold decides whether the machine should be kept from sleeping
// right now. Policy values: "ac" (default — only on mains), "always", "never".
func wantSystemHold(policy string, onAC bool) bool {
	switch policy {
	case "always":
		return true
	case "never":
		return false
	default:
		return onAC
	}
}

type systemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

// onMainsPower reports whether the machine runs on AC. Unknown (a desktop
// with no battery reports 1, but some firmware says 255) counts as mains:
// the safe failure for a remote host is staying reachable.
func onMainsPower() bool {
	var st systemPowerStatus
	if r, _, _ := wkProcGetSysPowerStatu.Call(uintptr(unsafe.Pointer(&st))); r == 0 {
		return true
	}
	return st.ACLineStatus != 0
}

// keepAwake is the system hold. It runs for the life of the process on its own
// pinned thread, re-evaluating the policy every keepAwakeInterval so plugging
// in or unplugging takes effect without a restart.
func keepAwake(policy func() string) {
	runtime.LockOSThread()
	held := false
	apply := func() {
		want := wantSystemHold(policy(), onMainsPower())
		if want == held {
			return
		}
		held = want
		if want {
			wkProcSetThreadExecSt.Call(wkEsContinuous | wkEsSystemRequired)
			log.Printf("[pc-remote] holding the system awake (on mains power) — the display may still turn off")
		} else {
			wkProcSetThreadExecSt.Call(wkEsContinuous)
			log.Printf("[pc-remote] released the system hold — sleep per the power plan")
		}
	}
	apply()
	for range time.Tick(keepAwakeInterval) {
		apply()
	}
}
