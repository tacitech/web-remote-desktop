//go:build windows

package main

import (
	"runtime"
	"testing"
	"time"
)

// The aggregate SystemExecutionState can't attribute a hold on a busy machine
// (any app can set the same bit), so test the refcount contract instead.
func TestDisplayHold(t *testing.T) {
	// Watch the observable contract: arrive → a hold starts; leave → it
	// releases. Exercise the refcount across the 1→2→1→0 path.
	viewerArrived()
	viewerArrived()
	viewerLeft()
	select {
	case <-viewStop:
		t.Fatal("hold released while a viewer remains")
	case <-time.After(100 * time.Millisecond):
	}
	viewerLeft()
	select {
	case <-viewStop:
		// released
	case <-time.After(time.Second):
		t.Fatal("hold not released after last viewer left")
	}
	if viewCount != 0 {
		t.Fatalf("viewCount = %d", viewCount)
	}
	// A new first viewer must start a fresh hold.
	viewerArrived()
	defer viewerLeft()
	select {
	case <-viewStop:
		t.Fatal("new hold born closed")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestWantSystemHold(t *testing.T) {
	cases := []struct {
		policy string
		onAC   bool
		want   bool
	}{
		{"ac", true, true}, {"ac", false, false},
		{"", true, true}, {"", false, false}, // unset = ac
		{"always", true, true}, {"always", false, true},
		{"never", true, false}, {"never", false, false},
	}
	for _, c := range cases {
		if got := wantSystemHold(c.policy, c.onAC); got != c.want {
			t.Errorf("wantSystemHold(%q, ac=%v) = %v, want %v", c.policy, c.onAC, got, c.want)
		}
	}
}

// SetThreadExecutionState returns the thread's previous state, so from the
// same pinned thread we can read back what the hold actually set.
func TestSystemHoldTakesOnThread(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	wkProcSetThreadExecSt.Call(wkEsContinuous | wkEsSystemRequired)
	prev, _, _ := wkProcSetThreadExecSt.Call(wkEsContinuous) // also releases
	if prev&wkEsSystemRequired == 0 {
		t.Fatalf("SYSTEM_REQUIRED not set on the thread; previous state 0x%x", prev)
	}
}
